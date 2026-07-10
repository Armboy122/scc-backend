package persistence

import (
	"context"
	"errors"

	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
)

// GormWorkHubRepo implements user.WorkHubRepository using GORM.
type GormWorkHubRepo struct{ db *gorm.DB }

// NewGormWorkHubRepo creates a new GormWorkHubRepo.
func NewGormWorkHubRepo(db *gorm.DB) *GormWorkHubRepo { return &GormWorkHubRepo{db: db} }

func (r *GormWorkHubRepo) FindByID(ctx context.Context, id string) (*user.WorkHub, error) {
	var m WorkHubModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toWorkHubDomain(&m), nil
}

func (r *GormWorkHubRepo) List(ctx context.Context) ([]*user.WorkHub, error) {
	var models []WorkHubModel
	if err := r.db.WithContext(ctx).Order("name").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]*user.WorkHub, len(models))
	for i := range models {
		result[i] = toWorkHubDomain(&models[i])
	}
	return result, nil
}

func (r *GormWorkHubRepo) Create(ctx context.Context, wh *user.WorkHub) error {
	m := &WorkHubModel{ID: wh.ID, Name: wh.Name}
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormWorkHubRepo) Update(ctx context.Context, wh *user.WorkHub) error {
	return r.db.WithContext(ctx).Model(&WorkHubModel{}).Where("id = ?", wh.ID).Update("name", wh.Name).Error
}

// GormOfficeRepo implements user.OfficeRepository using GORM.
type GormOfficeRepo struct{ db *gorm.DB }

// NewGormOfficeRepo creates a new GormOfficeRepo.
func NewGormOfficeRepo(db *gorm.DB) *GormOfficeRepo { return &GormOfficeRepo{db: db} }

func (r *GormOfficeRepo) FindByID(ctx context.Context, id string) (*user.Office, error) {
	var m OfficeModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toOfficeDomain(&m), nil
}

func (r *GormOfficeRepo) List(ctx context.Context) ([]*user.Office, error) {
	var models []OfficeModel
	if err := r.db.WithContext(ctx).Order("name").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]*user.Office, len(models))
	for i := range models {
		result[i] = toOfficeDomain(&models[i])
	}
	return result, nil
}

func (r *GormOfficeRepo) Create(ctx context.Context, o *user.Office) error {
	m := &OfficeModel{ID: o.ID, Name: o.Name, WorkHubID: o.WorkHubID}
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormOfficeRepo) Update(ctx context.Context, o *user.Office) error {
	return r.db.WithContext(ctx).Model(&OfficeModel{}).Where("id = ?", o.ID).Updates(map[string]interface{}{
		"name": o.Name, "work_hub_id": o.WorkHubID,
	}).Error
}
