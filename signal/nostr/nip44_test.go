// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The vectors below are a selection from the NIP-44 v2 test vectors published
// alongside the specification (github.com/paulmillr/nip44,
// nip44.vectors.json). They are outputs of the algorithm, copied as data so
// the tests need no network and no file of uncertain licence.

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func convFromHex(sec, pub string) ([32]byte, error) {
	k, err := SecretKeyFromHex(sec)
	if err != nil {
		return [32]byte{}, err
	}
	p, err := parsePublicKey(pub)
	if err != nil {
		return [32]byte{}, err
	}
	return conversationKey(k, p), nil
}

func TestNIP44ConversationKey(t *testing.T) {
	for _, v := range []struct{ sec, pub, conv string }{
		{"315e59ff51cb9209768cf7da80791ddcaae56ac9775eb25b6dee1234bc5d2268", "c2f9d9948dc8c7c38321e4b85c8558872eafa0641cd269db76848a6073e69133", "3dfef0ce2a4d80a25e7a328accf73448ef67096f65f79588e358d9a0eb9013f1"},
		{"a1e37752c9fdc1273be53f68c5f74be7c8905728e8de75800b94262f9497c86e", "03bb7947065dde12ba991ea045132581d0954f042c84e06d8c00066e23c1a800", "4d14f36e81b8452128da64fe6f1eae873baae2f444b02c950b90e43553f2178b"},
		{"98a5902fd67518a0c900f0fb62158f278f94a21d6f9d33d30cd3091195500311", "aae65c15f98e5e677b5050de82e3aba47a6fe49b3dab7863cf35d9478ba9f7d1", "9c00b769d5f54d02bf175b7284a1cbd28b6911b06cda6666b2243561ac96bad7"},
	} {
		got, err := convFromHex(v.sec, v.pub)
		if err != nil {
			t.Fatalf("%s: %v", v.sec, err)
		}
		if hex.EncodeToString(got[:]) != v.conv {
			t.Errorf("conversation key for %s = %x, want %s", v.sec, got, v.conv)
		}
	}
}

func TestNIP44ConversationKeyRefusesBadKeys(t *testing.T) {
	for _, v := range []struct{ sec, pub, note string }{
		{"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "sec1 higher than curve.n"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "sec1 is 0"},
		{"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364139", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "pub2 is invalid, no sqrt, all-ff"},
		{"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "sec1 == curve.n"},
		{"0000000000000000000000000000000000000000000000000000000000000002", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "pub2 is invalid, no sqrt"},
		{"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20", "0000000000000000000000000000000000000000000000000000000000000000", "pub2 is point of order 3 on twist"},
		{"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20", "eb1f7200aecaa86682376fb1c13cd12b732221e774f553b0a0857f88fa20f86d", "pub2 is point of order 13 on twist"},
		{"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20", "709858a4c121e4a84eb59c0ded0261093c71e8ca29efeef21a6161c447bcaf9f", "pub2 is point of order 3319 on twist"},
	} {
		if _, err := convFromHex(v.sec, v.pub); err == nil {
			t.Errorf("%s: accepted", v.note)
		}
	}
}

func TestNIP44PaddedLen(t *testing.T) {
	for _, v := range [][2]int{
		{16, 32}, {32, 32}, {33, 64}, {37, 64}, {45, 64}, {49, 64}, {64, 64}, {65, 96},
		{100, 128}, {111, 128}, {200, 224}, {250, 256}, {320, 320}, {383, 384}, {384, 384},
		{400, 448}, {500, 512}, {512, 512}, {515, 640}, {700, 768}, {800, 896}, {900, 1024},
		{1020, 1024}, {65536, 65536},
	} {
		if got := paddedLen(v[0]); got != v[1] {
			t.Errorf("paddedLen(%d) = %d, want %d", v[0], got, v[1])
		}
	}
}

func TestNIP44EncryptDecrypt(t *testing.T) {
	for _, v := range []struct{ sec1, sec2, conv, nonce, plaintext, payload string }{
		{"0000000000000000000000000000000000000000000000000000000000000001", "0000000000000000000000000000000000000000000000000000000000000002", "c41c775356fd92eadc63ff5a0dc1da211b268cbea22316767095b2871ea1412d", "0000000000000000000000000000000000000000000000000000000000000001", "a", "AgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABee0G5VSK0/9YypIObAtDKfYEAjD35uVkHyB0F4DwrcNaCXlCWZKaArsGrY6M9wnuTMxWfp1RTN9Xga8no+kF5Vsb"},
		{"0000000000000000000000000000000000000000000000000000000000000002", "0000000000000000000000000000000000000000000000000000000000000001", "c41c775356fd92eadc63ff5a0dc1da211b268cbea22316767095b2871ea1412d", "f00000000000000000000000000000f00000000000000000000000000000000f", "🍕🫃", "AvAAAAAAAAAAAAAAAAAAAPAAAAAAAAAAAAAAAAAAAAAPSKSK6is9ngkX2+cSq85Th16oRTISAOfhStnixqZziKMDvB0QQzgFZdjLTPicCJaV8nDITO+QfaQ61+KbWQIOO2Yj"},
		{"5c0c523f52a5b6fad39ed2403092df8cebc36318b39383bca6c00808626fab3a", "4b22aa260e4acb7021e32f38a6cdf4b673c6a277755bfce287e370c924dc936d", "3e2b52a63be47d34fe0a80e34e73d436d6963bc8f39827f327057a9986c20a45", "b635236c42db20f021bb8d1cdff5ca75dd1a0cc72ea742ad750f33010b24f73b", "表ポあA鷗ŒéＢ逍Üßªąñ丂㐀𠀀", "ArY1I2xC2yDwIbuNHN/1ynXdGgzHLqdCrXUPMwELJPc7s7JqlCMJBAIIjfkpHReBPXeoMCyuClwgbT419jUWU1PwaNl4FEQYKCDKVJz+97Mp3K+Q2YGa77B6gpxB/lr1QgoqpDf7wDVrDmOqGoiPjWDqy8KzLueKDcm9BVP8xeTJIxs="},
		{"8f40e50a84a7462e2b8d24c28898ef1f23359fff50d8c509e6fb7ce06e142f9c", "b9b0a1e9cc20100c5faa3bbe2777303d25950616c4c6a3fa2e3e046f936ec2ba", "d5a2f879123145a4b291d767428870f5a8d9e5007193321795b40183d4ab8c2b", "b20989adc3ddc41cd2c435952c0d59a91315d8c5218d5040573fc3749543acaf", "ability🤝的 ȺȾ", "ArIJia3D3cQc0sQ1lSwNWakTFdjFIY1QQFc/w3SVQ6yvbG2S0x4Yu86QGwPTy7mP3961I1XqB6SFFTzqDZZavhxoWMj7mEVGMQIsh2RLWI5EYQaQDIePSnXPlzf7CIt+voTD"},
	} {
		k2, err := SecretKeyFromHex(v.sec2)
		if err != nil {
			t.Fatal(err)
		}
		conv, err := convFromHex(v.sec1, k2.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(conv[:]) != v.conv {
			t.Fatalf("conversation key %x, want %s", conv, v.conv)
		}
		got, err := nip44EncryptWithNonce([]byte(v.plaintext), conv, mustHex(t, v.nonce))
		if err != nil {
			t.Fatal(err)
		}
		if got != v.payload {
			t.Errorf("encrypt %q = %s, want %s", v.plaintext, got, v.payload)
		}
		plain, err := nip44Decrypt(v.payload, conv)
		if err != nil {
			t.Fatalf("decrypt %q: %v", v.plaintext, err)
		}
		if string(plain) != v.plaintext {
			t.Errorf("decrypt = %q, want %q", plain, v.plaintext)
		}
	}
}

func TestNIP44LongMessages(t *testing.T) {
	for _, v := range []struct {
		conv, nonce, pattern string
		repeat               int
		payloadSHA           string
	}{
		{"8fc262099ce0d0bb9b89bac05bb9e04f9bc0090acc181fef6840ccee470371ed", "326bcb2c943cd6bb717588c9e5a7e738edf6ed14ec5f5344caa6ef56f0b9cff7", "x", 65535, "90714492225faba06310bff2f249ebdc2a5e609d65a629f1c87f2d4ffc55330a"},
		{"56adbe3720339363ab9c3b8526ffce9fd77600927488bfc4b59f7a68ffe5eae0", "ad68da81833c2a8ff609c3d2c0335fd44fe5954f85bb580c6a8d467aa9fc5dd0", "!", 65535, "8013e45a109fad3362133132b460a2d5bce235fe71c8b8f4014793fb52a49844"},
		{"7fc540779979e472bb8d12480b443d1e5eb1098eae546ef2390bee499bbf46be", "34905e82105c20de9a2f6cd385a0d541e6bcc10601d12481ff3a7575dc622033", "🦄", 16383, "b3348422471da1f3c59d79acfe2fe103f3cd24488109e5b18734cdb5953afd15"},
	} {
		var conv [32]byte
		copy(conv[:], mustHex(t, v.conv))
		plaintext := strings.Repeat(v.pattern, v.repeat)
		payload, err := nip44EncryptWithNonce([]byte(plaintext), conv, mustHex(t, v.nonce))
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256([]byte(payload)); hex.EncodeToString(sum[:]) != v.payloadSHA {
			t.Errorf("payload for %q x %d hashes to %x", v.pattern, v.repeat, sum)
		}
		plain, err := nip44Decrypt(payload, conv)
		if err != nil || string(plain) != plaintext {
			t.Errorf("round trip of %q x %d failed: %v", v.pattern, v.repeat, err)
		}
	}
}

func TestNIP44RefusesBadLengths(t *testing.T) {
	var conv [32]byte
	for _, n := range []int{0, 65536, 100000} {
		if _, err := nip44Encrypt(make([]byte, n), conv); err == nil {
			t.Errorf("encrypted a %d-byte plaintext", n)
		}
	}
}

func TestNIP44DecryptRefusesBadPayloads(t *testing.T) {
	for _, v := range []struct{ conv, payload, note string }{
		{"ca2527a037347b91bea0c8a30fc8d9600ffd81ec00038671e3a0f0cb0fc9f642", "#Atqupco0WyaOW2IGDKcshwxI9xO8HgD/P8Ddt46CbxDbrhdG8VmJdU0MIDf06CUvEvdnr1cp1fiMtlM/GrE92xAc1K5odTpCzUB+mjXgbaqtntBUbTToSUoT0ovrlPwzGjyp", "unknown encryption version"},
		{"36f04e558af246352dcf73b692fbd3646a2207bd8abd4b1cd26b234db84d9481", "AK1AjUvoYW3IS7C/BGRUoqEC7ayTfDUgnEPNeWTF/reBZFaha6EAIRueE9D1B1RuoiuFScC0Q94yjIuxZD3JStQtE8JMNacWFs9rlYP+ZydtHhRucp+lxfdvFlaGV/sQlqZz", "unknown encryption version 0"},
		{"ca2527a037347b91bea0c8a30fc8d9600ffd81ec00038671e3a0f0cb0fc9f642", "Atфupco0WyaOW2IGDKcshwxI9xO8HgD/P8Ddt46CbxDbrhdG8VmJZE0UICD06CUvEvdnr1cp1fiMtlM/GrE92xAc1EwsVCQEgWEu2gsHUVf4JAa3TpgkmFc3TWsax0v6n/Wq", "invalid base64"},
		{"cff7bd6a3e29a450fd27f6c125d5edeb0987c475fd1e8d97591e0d4d8a89763c", "Agn/l3ULCEAS4V7LhGFM6IGA17jsDUaFCKhrbXDANholyySBfeh+EN8wNB9gaLlg4j6wdBYh+3oK+mnxWu3NKRbSvQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "invalid MAC"},
		{"cfcc9cf682dfb00b11357f65bdc45e29156b69db424d20b3596919074f5bf957", "AmWxSwuUmqp9UsQX63U7OQ6K1thLI69L7G2b+j4DoIr0oRWQ8avl4OLqWZiTJ10vIgKrNqjoaX+fNhE9RqmR5g0f6BtUg1ijFMz71MO1D4lQLQfW7+UHva8PGYgQ1QpHlKgR", "invalid MAC"},
		{"5254827d29177622d40a7b67cad014fe7137700c3c523903ebbe3e1b74d40214", "Anq2XbuLvCuONcr7V0UxTh8FAyWoZNEdBHXvdbNmDZHB573MI7R7rrTYftpqmvUpahmBC2sngmI14/L0HjOZ7lWGJlzdh6luiOnGPc46cGxf08MRC4CIuxx3i2Lm0KqgJ7vA", "invalid padding"},
		{"fea39aca9aa8340c3a78ae1f0902aa7e726946e4efcd7783379df8096029c496", "An1Cg+O1TIhdav7ogfSOYvCj9dep4ctxzKtZSniCw5MwRrrPJFyAQYZh5VpjC2QYzny5LIQ9v9lhqmZR4WBYRNJ0ognHVNMwiFV1SHpvUFT8HHZN/m/QarflbvDHAtO6pY16", "invalid padding"},
		{"0c4cffb7a6f7e706ec94b2e879f1fc54ff8de38d8db87e11787694d5392d5b3f", "Am+f1yZnwnOs0jymZTcRpwhDRHTdnrFcPtsBzpqVdD6b2NZDaNm/TPkZGr75kbB6tCSoq7YRcbPiNfJXNch3Tf+o9+zZTMxwjgX/nm3yDKR2kHQMBhVleCB9uPuljl40AJ8kXRD0gjw+aYRJFUMK9gCETZAjjmrsCM+nGRZ1FfNsHr6Z", "invalid padding"},
		{"5cd2d13b9e355aeb2452afbd3786870dbeecb9d355b12cb0a3b6e9da5744cd35", "", "invalid payload length: 0"},
		{"d61d3f09c7dfe1c0be91af7109b60a7d9d498920c90cbba1e137320fdd938853", "Ag==", "invalid payload length: 4"},
		{"873bb0fc665eb950a8e7d5971965539f6ebd645c83c08cd6a85aafbad0f0bc47", "AqxgToSh3H7iLYRJjoWAM+vSv/Y1mgNlm6OWWjOYUClrFF8=", "invalid payload length: 48"},
		{"9f2fef8f5401ac33f74641b568a7a30bb19409c76ffdc5eae2db6b39d2617fbe", "Ap/2SEZCVFIhYk6qx7nqJxM6TMI1ZoKmAzrO7vBDVJhhuZXWiM20i/tIsbjT0KxkJs2MZjh1oXNYMO9ggfk7i47WQA==", "invalid payload length: 92"},
	} {
		var conv [32]byte
		copy(conv[:], mustHex(t, v.conv))
		if plain, err := nip44Decrypt(v.payload, conv); err == nil {
			t.Errorf("%s: decrypted to %q", v.note, plain)
		}
	}
}
