package middleware

import (
	"strings"

	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
	"gorm.io/gorm"
)

type casbinRule struct {
	ID    uint   `gorm:"primaryKey"`
	Ptype string `gorm:"size:100"`
	V0    string `gorm:"size:100"`
	V1    string `gorm:"size:100"`
	V2    string `gorm:"size:100"`
	V3    string `gorm:"size:100"`
	V4    string `gorm:"size:100"`
	V5    string `gorm:"size:100"`
}

func (casbinRule) TableName() string { return "casbin_rule" }

type postgresCasbinAdapter struct{ db *gorm.DB }

func newPostgresCasbinAdapter(db *gorm.DB) (*postgresCasbinAdapter, error) {
	a := &postgresCasbinAdapter{db: db}
	return a, db.AutoMigrate(&casbinRule{})
}
func (a *postgresCasbinAdapter) LoadPolicy(m model.Model) error {
	var rules []casbinRule
	if err := a.db.Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		values := []string{rule.Ptype, rule.V0, rule.V1, rule.V2, rule.V3, rule.V4, rule.V5}
		for len(values) > 1 && values[len(values)-1] == "" {
			values = values[:len(values)-1]
		}
		if err := persist.LoadPolicyArray(values, m); err != nil {
			return err
		}
	}
	return nil
}
func (a *postgresCasbinAdapter) SavePolicy(m model.Model) error {
	return a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&casbinRule{}).Error; err != nil {
			return err
		}
		for sec, astMap := range m {
			for ptype, ast := range astMap {
				for _, policy := range ast.Policy {
					if err := tx.Create(ruleFor(sec+"_"+ptype, policy)).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
func (a *postgresCasbinAdapter) AddPolicy(_ string, ptype string, rule []string) error {
	return a.db.Create(ruleFor(ptype, rule)).Error
}
func (a *postgresCasbinAdapter) RemovePolicy(_ string, ptype string, rule []string) error {
	query := a.db.Where("ptype = ?", ptype)
	for i, value := range rule {
		query = query.Where("v"+string(rune('0'+i))+" = ?", value)
	}
	return query.Delete(&casbinRule{}).Error
}
func (a *postgresCasbinAdapter) RemoveFilteredPolicy(_ string, ptype string, fieldIndex int, fieldValues ...string) error {
	query := a.db.Where("ptype = ?", ptype)
	for i, value := range fieldValues {
		if value != "" {
			query = query.Where("v"+string(rune('0'+fieldIndex+i))+" = ?", value)
		}
	}
	return query.Delete(&casbinRule{}).Error
}
func ruleFor(ptype string, values []string) casbinRule {
	values = append([]string{}, values...)
	for len(values) < 6 {
		values = append(values, "")
	}
	return casbinRule{Ptype: ptype, V0: strings.TrimSpace(values[0]), V1: strings.TrimSpace(values[1]), V2: strings.TrimSpace(values[2]), V3: strings.TrimSpace(values[3]), V4: strings.TrimSpace(values[4]), V5: strings.TrimSpace(values[5])}
}
