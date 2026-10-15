package nodehost

func value[T any](p *T) (v T) {
	if p != nil {
		return *p
	}
	return v
}
