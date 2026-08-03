package a2atape

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

var structureValidator = newStructureValidator()

func newStructureValidator() *validator.Validate {
	validate := validator.New(validator.WithRequiredStructEnabled())
	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
		if name == "" || name == "-" {
			return field.Name
		}
		return name
	})
	return validate
}

func validateStructure(name string, value any) error {
	if err := structureValidator.Struct(value); err != nil {
		return fmt.Errorf("a2a tape: validate %s: %w", name, err)
	}
	return nil
}

func validateValue(name string, value any, rules string) error {
	if err := structureValidator.Var(value, rules); err != nil {
		return fmt.Errorf("a2a tape: validate %s: %w", name, err)
	}
	return nil
}
