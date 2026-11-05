package records

import (
	"errors"
	"fmt"
	"strings"
)

// validateRecordDefs は、定義の一覧とやめた種類を検査します。誤りがあれば、起動を止めるためのエラーを返します。
// 不変条件: RECORDS-INV-2
func validateRecordDefs(defs []RecordDef, retired []RetiredDef) error {
	var errs []error
	names := map[string]bool{}
	for _, d := range defs {
		if err := validateRecordDef(d); err != nil {
			errs = append(errs, fmt.Errorf("records %q: %w", d.Name, err))
		}
		if names[d.Name] {
			errs = append(errs, fmt.Errorf("records %q: defined twice", d.Name))
		}
		names[d.Name] = true
	}
	retiredNames := map[string]bool{}
	for _, r := range retired {
		if !namePattern.MatchString(r.Name) || reservedDefNames[r.Name] {
			errs = append(errs, fmt.Errorf("retired records %q: invalid name", r.Name))
		}
		if names[r.Name] {
			errs = append(errs, fmt.Errorf("retired records %q: also in the definitions", r.Name))
		}
		if retiredNames[r.Name] {
			errs = append(errs, fmt.Errorf("retired records %q: listed twice", r.Name))
		}
		retiredNames[r.Name] = true
	}
	return errors.Join(errs...)
}

// validateRecordDef は、1つの種類を検査します。
func validateRecordDef(d RecordDef) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !namePattern.MatchString(d.Name) {
		fail("name must be lowercase letters, digits and _ (max 30), starting with a letter")
	}
	if reservedDefNames[d.Name] {
		fail("name %q is reserved", d.Name)
	}
	for label, v := range map[string]string{"Title": d.Title, "Purpose": d.Purpose, "Retention": d.Retention} {
		if strings.TrimSpace(v) == "" {
			fail("%s must not be empty", label)
		}
	}
	if d.Access < OwnerOnly || d.Access > AllReadWrite {
		fail("unknown Access %d", d.Access)
	}
	sc, err := replaySteps(d.Steps)
	if err != nil {
		fail("%v", err)
		return errors.Join(errs...)
	}
	if len(sc.columns) == 0 {
		fail("at least one field is required")
	}
	// 見せ方と、手順を当てた結果の項目を、過不足なく1対1で突き合わせる
	types := map[string]FieldType{}
	for _, c := range sc.columns {
		types[c.name] = c.typ
	}
	seen := map[string]bool{}
	for _, f := range d.Fields {
		if seen[f.Name] {
			fail("field %q is listed twice in Fields", f.Name)
			continue
		}
		seen[f.Name] = true
		t, ok := types[f.Name]
		if !ok {
			fail("field %q in Fields is not a column after the steps", f.Name)
			continue
		}
		if err := validateField(f, t); err != nil {
			fail("field %q: %v", f.Name, err)
		}
	}
	for _, c := range sc.columns {
		if !seen[c.name] {
			fail("column %q has no entry in Fields", c.name)
		}
	}
	for _, name := range d.ListFields {
		if _, ok := types[name]; !ok {
			fail("ListFields refers to unknown field %q", name)
		}
	}
	if d.SortBy != "" {
		if _, ok := types[d.SortBy]; !ok {
			fail("SortBy refers to unknown field %q", d.SortBy)
		}
	}
	return errors.Join(errs...)
}

// validateField は、項目の見せ方が型に合うかを検査します。
func validateField(f RecordField, t FieldType) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if strings.TrimSpace(f.Label) == "" {
		fail("Label must not be empty")
	}
	if f.MaxLen != 0 && t != Text && t != LongText {
		fail("MaxLen is only for text and long_text")
	}
	if f.MaxLen < 0 {
		fail("MaxLen must not be negative")
	}
	if (f.Min != nil || f.Max != nil) && t != Integer {
		fail("Min and Max are only for integer")
	}
	if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
		fail("Min must not be greater than Max")
	}
	if t == Select {
		if len(f.Options) == 0 {
			fail("select needs Options")
		}
		opts := map[string]bool{}
		for _, o := range f.Options {
			if strings.TrimSpace(o) == "" || strings.ContainsAny(o, "\r\n\t") {
				fail("option %q must be a non-empty single line", o)
			}
			if opts[o] {
				fail("option %q is listed twice", o)
			}
			opts[o] = true
		}
	} else if len(f.Options) > 0 {
		fail("Options are only for select")
	}
	return errors.Join(errs...)
}
