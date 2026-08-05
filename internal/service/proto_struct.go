package service

import "google.golang.org/protobuf/types/known/structpb"

// structToMap converts a protobuf Struct to a plain map. Nil and empty
// structures are normalized to nil so domain requests do not carry empty
// attribute bags.
func structToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	m := s.AsMap()
	if len(m) == 0 {
		return nil
	}
	return m
}

// mapToStruct converts a plain map to a protobuf Struct. Invalid values are
// treated as absent metadata; transport handlers must not fail an otherwise
// valid response because optional audit metadata cannot be represented.
func mapToStruct(m map[string]any) *structpb.Struct {
	if len(m) == 0 {
		return nil
	}
	st, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return st
}
