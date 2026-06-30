package service

import (
	"errors"
	"strings"

	"atoman/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type OwnerBootstrapInput struct {
	Username string
	Email    string
	Password string
}

type OwnerBootstrapService struct {
	db *gorm.DB
}

func NewOwnerBootstrapService(db *gorm.DB) *OwnerBootstrapService {
	return &OwnerBootstrapService{db: db}
}

func (s *OwnerBootstrapService) EnsureOwner(input OwnerBootstrapInput) (model.User, bool, error) {
	username := strings.TrimSpace(input.Username)
	email := strings.TrimSpace(input.Email)
	password := input.Password

	if username == "" || email == "" || password == "" {
		return model.User{}, false, errors.New("owner username, email, and password are required")
	}
	if len(password) < 6 {
		return model.User{}, false, errors.New("owner password must be at least 6 characters")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, false, err
	}

	var ensured model.User
	created := false

	err = s.db.Transaction(func(tx *gorm.DB) error {
		usernameUser, usernameFound, err := findOwnerBootstrapUserBy(tx, "username", username)
		if err != nil {
			return err
		}
		emailUser, emailFound, err := findOwnerBootstrapUserBy(tx, "email", email)
		if err != nil {
			return err
		}

		var user model.User
		switch {
		case usernameFound && emailFound:
			if usernameUser.UUID != emailUser.UUID {
				return errors.New("owner username and email belong to different users")
			}
			user = usernameUser
		case usernameFound || emailFound:
			return errors.New("owner username and email must match the same user")
		default:
			user = model.User{
				Username: username,
				Email:    email,
				Password: string(hashedPassword),
				Role:     "owner",
				IsActive: true,
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			created = true
		}

		if !created {
			updates := map[string]any{
				"username":  username,
				"email":     email,
				"password":  string(hashedPassword),
				"role":      "owner",
				"is_active": true,
			}
			if err := tx.Model(&user).Updates(updates).Error; err != nil {
				return err
			}
		}

		if err := tx.Model(&model.User{}).
			Where("uuid <> ? AND role = ?", user.UUID, "owner").
			Update("role", "admin").Error; err != nil {
			return err
		}

		if err := tx.FirstOrCreate(&model.UserSettings{UserID: user.UUID}, model.UserSettings{UserID: user.UUID}).Error; err != nil {
			return err
		}

		if err := NewUserBootstrapService(tx).EnsureDefaults(user.UUID, user.Username); err != nil {
			return err
		}

		if err := tx.Where("uuid = ?", user.UUID).First(&ensured).Error; err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return model.User{}, false, err
	}

	return ensured, created, nil
}

func findOwnerBootstrapUserBy(tx *gorm.DB, field string, value string) (model.User, bool, error) {
	var user model.User
	err := tx.Where(field+" = ?", value).First(&user).Error
	if err == nil {
		return user, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, false, nil
	}
	return model.User{}, false, err
}
