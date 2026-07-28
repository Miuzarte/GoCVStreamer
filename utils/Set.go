package utils

func NewSet[T comparable](elems ...T) Set[T] {
	s := make(Set[T], len(elems))
	for _, elem := range elems {
		s[elem] = struct{}{}
	}
	return s
}

type Set[T comparable] map[T]struct{}

func (s Set[T]) Has1(elem T) bool {
	_, ok := s[elem]
	return ok
}

func (s Set[T]) Has(elems ...T) bool {
	for _, elem := range elems {
		if _, ok := s[elem]; ok {
			return ok
		}
	}
	return false
}
