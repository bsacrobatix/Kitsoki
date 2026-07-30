package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/delivery"
	"kitsoki/internal/capsule/queue"
)

func deliveryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delivery",
		Short: "Manage durable bundle delivery through the Capsule merge queue",
	}
	cmd.AddCommand(
		deliverySubmitCmd(),
		deliveryGetCmd(),
		deliveryStatusCmd(),
		deliveryOpCmd("retry", "Retry recoverable delivery work", delivery.Service.Retry),
		deliveryOpCmd("cancel", "Park delivery work without losing its retained bundle", delivery.Service.Cancel),
		deliveryOpCmd("reject", "Terminally reject delivery work while retaining evidence", delivery.Service.Reject),
	)
	return cmd
}

func deliverySubmitCmd() *cobra.Command {
	var project, queueRoot, requestPath, bundlePath string
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Admit a retained durable_bundle request",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var request delivery.SubmitRequest
			if err := readDeliveryRequest(requestPath, &request); err != nil {
				return err
			}
			if strings.TrimSpace(bundlePath) != "" {
				request.BundlePath = bundlePath
			}
			result, err := deliveryService(project, queueRoot).Submit(cmd.Context(), request)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	deliveryStoreFlags(cmd, &project, &queueRoot)
	cmd.Flags().StringVar(&requestPath, "request", "", "strict delivery SubmitRequest JSON")
	cmd.Flags().StringVar(&bundlePath, "bundle", "", "override the already-downloaded bundle path in the request")
	_ = cmd.MarkFlagRequired("request")
	return cmd
}

func deliveryGetCmd() *cobra.Command {
	var project, queueRoot string
	cmd := &cobra.Command{
		Use:   "get <candidate-id>",
		Short: "Get one durable delivery candidate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := deliveryService(project, queueRoot).Get(args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	deliveryStoreFlags(cmd, &project, &queueRoot)
	return cmd
}

func deliveryStatusCmd() *cobra.Command {
	var project, queueRoot string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List durable delivery candidates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deliveryService(project, queueRoot).Status()
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	deliveryStoreFlags(cmd, &project, &queueRoot)
	return cmd
}

func deliveryOpCmd(
	verb, short string,
	run func(delivery.Service, queue.Op) (delivery.Result, error),
) *cobra.Command {
	var project, queueRoot, actor, reason string
	cmd := &cobra.Command{
		Use:   verb + " <candidate-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(actor) == "" {
				actor = os.Getenv("USER")
			}
			result, err := run(deliveryService(project, queueRoot), queue.Op{
				ID: args[0], Actor: actor, Reason: reason,
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	deliveryStoreFlags(cmd, &project, &queueRoot)
	cmd.Flags().StringVar(&actor, "actor", "", "acting operator recorded in evidence (defaults to $USER)")
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded in durable evidence")
	return cmd
}

func deliveryService(project, queueRoot string) delivery.Service {
	return delivery.New(queue.Store{
		ProjectRoot: project,
		QueueRoot:   queueRoot,
		LockWait:    2 * time.Second,
	})
}

func deliveryStoreFlags(cmd *cobra.Command, project, queueRoot *string) {
	cmd.Flags().StringVar(project, "project", ".", "project root")
	cmd.Flags().StringVar(queueRoot, "queue-root", "", "exact queue authority directory (default <project>/.capsules/queue)")
}

func readDeliveryRequest(path string, out *delivery.SubmitRequest) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("delivery: open request: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("delivery: parse request: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("delivery: parse request: trailing JSON value")
	}
	return nil
}
