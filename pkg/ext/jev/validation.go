package jev

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

var structureValidator = validator.New(validator.WithRequiredStructEnabled())

func validateStructure(name string, value any) error {
	if err := structureValidator.Struct(value); err != nil {
		return fmt.Errorf("jev: validate %s: %w", name, err)
	}
	return nil
}
