package threshold

import (
	"context"
	"errors"

	"traffic-center/domain"
)

// Usecase reads and writes per-project thresholds.
type Usecase struct {
	thresholds domain.ThresholdRepository
	projects   domain.ProjectRepository
}

// NewUsecase wires the threshold usecase.
func NewUsecase(thresholds domain.ThresholdRepository, projects domain.ProjectRepository) *Usecase {
	return &Usecase{thresholds: thresholds, projects: projects}
}

// Get returns the stored thresholds, falling back to defaults when the project
// has never had them written.
func (u *Usecase) Get(ctx context.Context, projectID string) (domain.Threshold, error) {
	p, err := u.projects.Get(ctx, projectID)
	if err != nil {
		return domain.Threshold{}, err
	}
	if identity, ok := domain.IdentityFrom(ctx); ok && !p.CanAccess(identity) {
		return domain.Threshold{}, domain.ErrNotFound
	}

	thresholds, err := u.thresholds.Get(ctx, projectID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.DefaultThreshold(), nil
	}
	if err != nil {
		return domain.Threshold{}, err
	}
	return thresholds, nil
}

// Update validates and upserts thresholds.
func (u *Usecase) Update(ctx context.Context, projectID string, thresholds domain.Threshold) error {
	p, err := u.projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if identity, ok := domain.IdentityFrom(ctx); ok && !p.CanAccess(identity) {
		return domain.ErrNotFound
	}
	if err := thresholds.Validate(); err != nil {
		return err
	}
	return u.thresholds.Upsert(ctx, projectID, thresholds)
}
