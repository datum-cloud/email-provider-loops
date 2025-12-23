package util

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StatusPatchParams holds the parameters for patching status.
type StatusPatchParams struct {
	Client     client.Client
	Logger     logr.Logger
	Object     client.Object
	Original   client.Object
	OldStatus  any
	NewStatus  any
	FieldOwner string
}

// PatchStatusIfChanged checks if the status has changed and patches it if so.
func PatchStatusIfChanged(ctx context.Context, params StatusPatchParams) error {
	if !equality.Semantic.DeepEqual(params.OldStatus, params.NewStatus) {
		if err := params.Client.Status().Patch(ctx, params.Object, client.MergeFrom(params.Original), client.FieldOwner(params.FieldOwner)); err != nil {
			params.Logger.Error(err, "Failed to patch resource status")
			return fmt.Errorf("failed to patch resource status: %w", err)
		}
	} else {
		params.Logger.Info("Resource status unchanged, skipping update")
	}

	return nil
}
