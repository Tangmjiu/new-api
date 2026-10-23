package model

import (
	"errors"
	"gorm.io/gorm"
)

// Free is a token access feature, never a user's membership or a subscription
// upgrade. The hook also covers bulk GORM updates using a map destination.
func (user *User) BeforeSave(tx *gorm.DB) error {
	if user.Group == FreeGroupName {
		return errors.New("free is reserved for the Free access module")
	}
	if update, ok := tx.Statement.Dest.(map[string]any); ok {
		if group, ok := update["group"].(string); ok && group == FreeGroupName {
			return errors.New("users cannot be assigned to the Free access module as a group")
		}
	}
	return nil
}
