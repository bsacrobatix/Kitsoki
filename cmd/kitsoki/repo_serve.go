package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/gitbackup"
	"kitsoki/internal/gitserve"
	"kitsoki/internal/objectstore"
)

// repoServeOnReady, when non-nil, receives the bound listen address once the
// server is accepting connections. Test seam: lets tests use --addr with port
// 0 and discover the real port.
var repoServeOnReady func(addr string)

func repoServeCmd() *cobra.Command {
	var bucket repoBucketFlags
	var root, addr, backupPrefix string
	var readOnly, backup, allowUnauthenticated bool
	cmd := &cobra.Command{
		Use:          "serve",
		Short:        "Serve a root of bare repositories over git smart HTTP",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			logf := func(format string, a ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), "kitsoki repo serve: "+format+"\n", a...)
			}
			opts := []gitserve.Option{}
			if readOnly {
				opts = append(opts, gitserve.WithReadOnly())
			}
			var queue *repoBackupQueue
			if backup {
				if readOnly {
					return fmt.Errorf("--backup is pointless with --read-only: pushes are rejected, so refs never change")
				}
				store, err := bucket.store()
				if err != nil {
					return err
				}
				queue = newRepoBackupQueue(repoBackupRunner(root, store, backupPrefix), logf)
				opts = append(opts, gitserve.WithRefHook(gitserve.RefHookFunc(
					func(repo string, updates []gitserve.RefUpdate) { queue.Notify(repo) },
				)))
			} else if cmd.Flags().Changed("bucket-url") {
				return fmt.Errorf("--bucket-url requires --backup")
			}
			handler, err := gitserve.New(root, opts...)
			if err != nil {
				return err
			}

			if !allowUnauthenticated {
				if err := requireLoopbackAddr(addr); err != nil {
					return err
				}
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}
			httpSrv := &http.Server{
				Handler: handler,
				// ReadHeaderTimeout guards against Slowloris; no WriteTimeout
				// because large clones can legitimately stream for minutes.
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       120 * time.Second,
			}
			serveCtx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			go func() {
				<-serveCtx.Done()
				shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutCancel()
				_ = httpSrv.Shutdown(shutCtx)
			}()

			logf("serving %s on http://%s (read-only=%v backup=%v)", root, ln.Addr(), readOnly, backup)
			if repoServeOnReady != nil {
				repoServeOnReady(ln.Addr().String())
			}
			serveErr := httpSrv.Serve(ln)
			if queue != nil {
				queue.Wait() // drain in-flight backups before exiting
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				return fmt.Errorf("serve: %w", serveErr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "serving root directory of bare repositories (required)")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9418", "HTTP listen address (loopback only unless --allow-unauthenticated)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "reject pushes; serve fetches and clones only")
	cmd.Flags().BoolVar(&allowUnauthenticated, "allow-unauthenticated", false,
		"permit binding a non-loopback address despite the server having no authentication: anyone who can reach the port gets anonymous read (and, without --read-only, write) access to every repository under --root")
	cmd.Flags().BoolVar(&backup, "backup", false, "back up each pushed repository to object storage after refs change")
	cmd.Flags().StringVar(&backupPrefix, "backup-prefix", "repos/", "object-store prefix under which each repository's bundle chain is stored")
	bucket.register(cmd, false)
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if backup && bucket.bucketURL == "" {
			return fmt.Errorf("--backup requires --bucket-url")
		}
		return nil
	}
	_ = cmd.MarkFlagRequired("root")
	return cmd
}

// requireLoopbackAddr rejects listen addresses that would expose the
// unauthenticated git server beyond this host. The gitserve handler's only
// access control without an injected AuthFunc is the bind address, so a
// non-loopback bind means anonymous network read/write to every served repo.
func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse --addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--addr %q is not a loopback address: this server has no authentication, so binding it beyond localhost exposes anonymous access to every repository under --root; use 127.0.0.1, or pass --allow-unauthenticated to accept that", addr)
}

// repoBackupRunner returns the function the backup queue invokes for one
// repository: an incremental backup of <root>/<repo> into
// <backupPrefix><repo>. Errors are returned to the queue, which logs them;
// a failed backup never blocks or fails the push that triggered it.
func repoBackupRunner(root string, store objectstore.Store, backupPrefix string) func(ctx context.Context, repo string) error {
	return func(ctx context.Context, repo string) error {
		prefix := path.Join(backupPrefix, repo)
		backer, err := gitbackup.Open(store, prefix, filepath.Join(root, filepath.FromSlash(repo)))
		if err != nil {
			return err
		}
		_, err = backer.BackupIncremental(ctx)
		return err
	}
}

// repoBackupQueue serializes backups per repository and coalesces bursts:
// at most one backup runs per repo at a time, and any number of
// notifications arriving while one runs collapse into a single follow-up
// run. Notify never blocks, so the push response is never delayed by backup
// work. Failures are logged, not propagated.
type repoBackupQueue struct {
	run  func(ctx context.Context, repo string) error
	logf func(format string, args ...any)

	mu      sync.Mutex
	running map[string]bool // repo currently being backed up
	dirty   map[string]bool // repo notified again while running
	wg      sync.WaitGroup
}

func newRepoBackupQueue(run func(ctx context.Context, repo string) error, logf func(string, ...any)) *repoBackupQueue {
	return &repoBackupQueue{
		run:     run,
		logf:    logf,
		running: make(map[string]bool),
		dirty:   make(map[string]bool),
	}
}

// Notify schedules an incremental backup of repo. Safe for concurrent use;
// returns immediately.
func (q *repoBackupQueue) Notify(repo string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running[repo] {
		q.dirty[repo] = true // coalesce: one follow-up run covers the burst
		return
	}
	q.running[repo] = true
	q.wg.Add(1)
	go q.worker(repo)
}

func (q *repoBackupQueue) worker(repo string) {
	defer q.wg.Done()
	for {
		if err := q.run(context.Background(), repo); err != nil {
			q.logf("backup %s: %v", repo, err)
		}
		q.mu.Lock()
		if q.dirty[repo] {
			delete(q.dirty, repo)
			q.mu.Unlock()
			continue
		}
		delete(q.running, repo)
		q.mu.Unlock()
		return
	}
}

// Wait blocks until every scheduled backup has finished.
func (q *repoBackupQueue) Wait() { q.wg.Wait() }
