package bdd

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// pauseContainer freezes or resumes a container's processes.
//
// testcontainers has no pause strategy of its own, so this reaches for the
// Docker client the library already owns rather than shelling out. It is still
// the library's client and the library's container id: what is being avoided is
// a second way of knowing which container a scenario is talking about.
//
// Pausing is what `service "payment-provider" is stopped` means. The provider
// keeps its events and their delivery attempts in process memory, so a
// recreated container is a provider that never heard of this scenario.
func pauseContainer(ctx context.Context, id string, pause bool) error {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return fmt.Errorf("docker provider: %w", err)
	}
	defer provider.Close()

	cli := provider.Client()
	if pause {
		if _, err := cli.ContainerPause(ctx, id, client.ContainerPauseOptions{}); err != nil {
			return err
		}
		return nil
	}
	if _, err := cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{}); err != nil {
		return err
	}
	return nil
}
