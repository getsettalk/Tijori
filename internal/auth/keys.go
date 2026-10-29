package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/state"
)

const (
	upperAlnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	alnum      = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// RandomString returns n characters drawn uniformly from alphabet.
func RandomString(n int, alphabet string) string {
	var b strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b.WriteByte(alphabet[v.Int64()])
	}
	return b.String()
}

// NewKey creates a fresh access key: "tj_" + 16 characters, with a
// 40-character secret.
func NewKey(name string, buckets []string, perm string) (state.AccessKey, error) {
	switch perm {
	case state.PermRead, state.PermWrite, state.PermReadWrite:
	case "":
		perm = state.PermReadWrite
	default:
		return state.AccessKey{}, fmt.Errorf("permission must be read, write or readwrite, not %q", perm)
	}
	if len(buckets) == 0 {
		buckets = []string{"*"}
	}
	return state.AccessKey{
		AccessKey:  "tj_" + RandomString(16, upperAlnum),
		SecretKey:  RandomString(40, alnum),
		Name:       name,
		Buckets:    buckets,
		Permission: perm,
		CreatedAt:  time.Now().UTC(),
	}, nil
}
