package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/admissionserver"
	"kitsoki/internal/objectstore"
)

func queueAdmissionServeCmd() *cobra.Command {
	var project, projectID, root, listen, tokenEnv, bucketURL, keyEnv, secretEnv, certFile, keyFile string
	var maxBundleBytes int64
	var maxConcurrent int
	cmd := &cobra.Command{
		Use:          "serve-admission",
		Short:        "Serve authenticated remote worker admission into external queue authority storage",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := strings.TrimSpace(os.Getenv(tokenEnv))
			if token == "" {
				return fmt.Errorf("queue admission: authentication token is missing from %s", tokenEnv)
			}
			spacesConfig, err := objectstore.ParseBucketURL(bucketURL)
			if err != nil {
				return err
			}
			spacesConfig.KeyEnv, spacesConfig.SecretEnv = keyEnv, secretEnv
			objects, err := objectstore.NewSpaces(spacesConfig, nil)
			if err != nil {
				return err
			}
			server, err := admissionserver.New(admissionserver.Config{
				Root: root, ProjectRoot: project, ProjectID: projectID,
				Token: token, RequireAuth: true, Objects: objects,
				MaxBundleBytes: maxBundleBytes, MaxConcurrent: maxConcurrent,
			})
			if err != nil {
				return err
			}
			if (certFile == "") != (keyFile == "") {
				return fmt.Errorf("queue admission: --tls-cert and --tls-key must be supplied together")
			}
			if certFile == "" && !loopbackListen(listen) {
				return fmt.Errorf("queue admission: plaintext listener must be loopback-only; use an SSH tunnel or configure TLS")
			}
			httpServer := &http.Server{
				Addr: listen, Handler: server.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      10 * time.Minute,
				IdleTimeout:       2 * time.Minute,
			}
			if certFile != "" {
				return httpServer.ListenAndServeTLS(certFile, keyFile)
			}
			return httpServer.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "protected project root (read-only during admission)")
	cmd.Flags().StringVar(&projectID, "project-id", "", "optional exact Capsule CI receipt project_id admitted by this service")
	cmd.Flags().StringVar(&root, "root", "", "durable service authority root outside the protected project checkout")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:7444", "HTTP listen address (plaintext must be loopback-only)")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "KITSOKI_QUEUE_ADMISSION_TOKEN", "environment variable holding the bearer token")
	cmd.Flags().StringVar(&bucketURL, "bucket-url", "", "virtual-hosted Spaces/S3 bucket URL containing worker outputs")
	cmd.Flags().StringVar(&keyEnv, "bucket-key-env", "KITSOKI_WORKER_OUTPUTS_ACCESS_KEY", "environment variable holding the Spaces access key ID")
	cmd.Flags().StringVar(&secretEnv, "bucket-secret-env", "KITSOKI_WORKER_OUTPUTS_SECRET_KEY", "environment variable holding the Spaces secret key")
	cmd.Flags().StringVar(&certFile, "tls-cert", "", "optional TLS certificate; required with --tls-key for non-loopback listeners")
	cmd.Flags().StringVar(&keyFile, "tls-key", "", "optional TLS private key")
	cmd.Flags().Int64Var(&maxBundleBytes, "max-bundle-bytes", 0, "maximum accepted remote Git bundle bytes (default 512 MiB)")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 4, "maximum simultaneous authenticated admissions before HTTP 429")
	_ = cmd.MarkFlagRequired("root")
	_ = cmd.MarkFlagRequired("bucket-url")
	return cmd
}

func loopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
