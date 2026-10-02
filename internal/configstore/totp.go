package configstore

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"time"
)

func encodeTOTPSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}

func totpCode(secret []byte, counter int64) string {
	var moving [8]byte
	binary.BigEndian.PutUint64(moving[:], uint64(counter))
	mac := hmac.New(sha1.New, secret)
	mac.Write(moving[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

// RFC 6238: tolerate one adjacent 30-second interval, but never reuse a code.
func verifyTOTP(secret []byte, code string, now time.Time, lastCounter int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, offset := range []int64{0, -1, 1} {
		counter := now.Unix()/30 + offset
		if counter > lastCounter && subtle.ConstantTimeCompare([]byte(totpCode(secret, counter)), []byte(code)) == 1 {
			return counter, true
		}
	}
	return 0, false
}
