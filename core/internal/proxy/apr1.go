package proxy

import (
	"crypto/md5"
	"crypto/rand"
)

const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// apr1 实现 Apache 的 MD5-crypt（$apr1$），squid 的 basic_ncsa_auth 能识别。
func apr1(password, salt string) string {
	const magic = "$apr1$"
	if len(salt) > 8 {
		salt = salt[:8]
	}
	pw := []byte(password)
	ctx := md5.New()
	ctx.Write(pw)
	ctx.Write([]byte(magic))
	ctx.Write([]byte(salt))

	alt := md5.New()
	alt.Write(pw)
	alt.Write([]byte(salt))
	alt.Write(pw)
	altSum := alt.Sum(nil)
	for i := len(pw); i > 0; i -= 16 {
		ctx.Write(altSum[:min(16, i)])
	}
	for i := len(pw); i > 0; i >>= 1 {
		if i&1 != 0 {
			ctx.Write([]byte{0})
		} else {
			ctx.Write(pw[:1])
		}
	}
	final := ctx.Sum(nil)
	for i := 0; i < 1000; i++ {
		c := md5.New()
		if i&1 != 0 {
			c.Write(pw)
		} else {
			c.Write(final)
		}
		if i%3 != 0 {
			c.Write([]byte(salt))
		}
		if i%7 != 0 {
			c.Write(pw)
		}
		if i&1 != 0 {
			c.Write(final)
		} else {
			c.Write(pw)
		}
		final = c.Sum(nil)
	}
	out := []byte(magic + salt + "$")
	to64 := func(v uint32, n int) {
		for ; n > 0; n-- {
			out = append(out, itoa64[v&0x3f])
			v >>= 6
		}
	}
	f := final
	to64(uint32(f[0])<<16|uint32(f[6])<<8|uint32(f[12]), 4)
	to64(uint32(f[1])<<16|uint32(f[7])<<8|uint32(f[13]), 4)
	to64(uint32(f[2])<<16|uint32(f[8])<<8|uint32(f[14]), 4)
	to64(uint32(f[3])<<16|uint32(f[9])<<8|uint32(f[15]), 4)
	to64(uint32(f[4])<<16|uint32(f[10])<<8|uint32(f[5]), 4)
	to64(uint32(f[11]), 2)
	return string(out)
}

func randomSalt() string {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = itoa64[int(b[i])%len(itoa64)]
	}
	return string(b)
}
