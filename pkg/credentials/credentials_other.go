//go:build !darwin

package credentials

func Set(host, user, kind string) error {
	return ErrUnsupported
}

func Get(host, user, kind string) error {
	return ErrUnsupported
}

func Delete(host, user, kind string) error {
	return ErrUnsupported
}
