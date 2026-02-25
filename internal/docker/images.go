package docker

import (
	"context"

	"github.com/moby/moby/client"
)

// ListImages returns all images with their tags, size, and usage status.
func (c *Client) ListImages(ctx context.Context) ([]ImageSummary, error) {
	result, err := c.api.ImageList(ctx, client.ImageListOptions{All: false})
	if err != nil {
		return nil, err
	}

	// Build a set of image IDs in use by containers (running or stopped).
	containers, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	usedImages := make(map[string]bool)
	for _, cont := range containers.Items {
		usedImages[cont.ImageID] = true
	}

	summaries := make([]ImageSummary, 0, len(result.Items))
	for _, img := range result.Items {
		summaries = append(summaries, ImageSummary{
			ID:       img.ID,
			RepoTags: img.RepoTags,
			Size:     img.Size,
			Created:  img.Created,
			InUse:    usedImages[img.ID],
		})
	}
	return summaries, nil
}

// PruneImages removes dangling (unused) images.
func (c *Client) PruneImages(ctx context.Context) (ImagePruneResult, error) {
	report, err := c.api.ImagePrune(ctx, client.ImagePruneOptions{})
	if err != nil {
		return ImagePruneResult{}, err
	}
	return ImagePruneResult{
		ImagesDeleted:  len(report.Report.ImagesDeleted),
		SpaceReclaimed: int64(report.Report.SpaceReclaimed), //nolint:gosec // space reclaimed won't exceed int64 max
	}, nil
}

// RemoveImageByID removes an image by its ID, pruning untagged children.
func (c *Client) RemoveImageByID(ctx context.Context, id string) error {
	_, err := c.api.ImageRemove(ctx, id, client.ImageRemoveOptions{PruneChildren: true})
	return err
}
