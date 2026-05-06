package services

import (
	"fmt"
	"strings"
)

const lzStringURISafeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-$"

// decompressLZStringFromEncodedURIComponent decodes payloads produced by
// lz-string's compressToEncodedURIComponent. DMM hashlists store their JSON in
// that exact format after the /hashlist# fragment.
func decompressLZStringFromEncodedURIComponent(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", nil
	}

	alphabet := make(map[rune]int, len(lzStringURISafeAlphabet))
	for idx, r := range lzStringURISafeAlphabet {
		alphabet[r] = idx
	}

	values := make([]int, 0, len(input))
	for _, r := range input {
		value, ok := alphabet[r]
		if !ok {
			return "", fmt.Errorf("invalid lz-string URI character %q", r)
		}
		values = append(values, value)
	}

	reader := lzBitReader{
		values:    values,
		position:  32,
		value:     values[0],
		nextIndex: 1,
	}

	readBits := func(count int) (int, bool) {
		bits := 0
		power := 1
		maxPower := 1 << count
		for power != maxPower {
			if reader.nextIndex > len(reader.values) && reader.position == 0 {
				return 0, false
			}
			resb := reader.value & reader.position
			reader.position >>= 1
			if reader.position == 0 {
				reader.position = 32
				if reader.nextIndex < len(reader.values) {
					reader.value = reader.values[reader.nextIndex]
					reader.nextIndex++
				} else {
					reader.value = 0
				}
			}
			if resb > 0 {
				bits |= power
			}
			power <<= 1
		}
		return bits, true
	}

	next, ok := readBits(2)
	if !ok {
		return "", fmt.Errorf("invalid lz-string payload")
	}

	var first string
	switch next {
	case 0:
		code, ok := readBits(8)
		if !ok {
			return "", fmt.Errorf("invalid lz-string literal")
		}
		first = string(rune(code))
	case 1:
		code, ok := readBits(16)
		if !ok {
			return "", fmt.Errorf("invalid lz-string literal")
		}
		first = string(rune(code))
	case 2:
		return "", nil
	default:
		return "", fmt.Errorf("invalid lz-string marker")
	}

	dictionary := map[int]string{
		0: "",
		1: "",
		2: "",
		3: first,
	}
	enlargeIn := 4
	dictSize := 4
	numBits := 3
	w := first
	result := strings.Builder{}
	result.WriteString(first)

	for {
		if reader.nextIndex > len(reader.values) && reader.position == 32 {
			return "", fmt.Errorf("unterminated lz-string payload")
		}

		c, ok := readBits(numBits)
		if !ok {
			return "", fmt.Errorf("invalid lz-string code")
		}

		switch c {
		case 0:
			code, ok := readBits(8)
			if !ok {
				return "", fmt.Errorf("invalid lz-string 8-bit literal")
			}
			dictionary[dictSize] = string(rune(code))
			c = dictSize
			dictSize++
			enlargeIn--
		case 1:
			code, ok := readBits(16)
			if !ok {
				return "", fmt.Errorf("invalid lz-string 16-bit literal")
			}
			dictionary[dictSize] = string(rune(code))
			c = dictSize
			dictSize++
			enlargeIn--
		case 2:
			return result.String(), nil
		}

		if enlargeIn == 0 {
			enlargeIn = 1 << numBits
			numBits++
		}

		entry, exists := dictionary[c]
		if !exists {
			if c != dictSize {
				return "", fmt.Errorf("invalid lz-string dictionary reference %d", c)
			}
			firstRune, ok := firstRune(w)
			if !ok {
				return "", fmt.Errorf("invalid empty lz-string entry")
			}
			entry = w + firstRune
		}

		result.WriteString(entry)

		firstRune, ok := firstRune(entry)
		if !ok {
			return "", fmt.Errorf("invalid empty lz-string dictionary entry")
		}
		dictionary[dictSize] = w + firstRune
		dictSize++
		enlargeIn--
		w = entry

		if enlargeIn == 0 {
			enlargeIn = 1 << numBits
			numBits++
		}
	}
}

type lzBitReader struct {
	values    []int
	position  int
	value     int
	nextIndex int
}

func firstRune(value string) (string, bool) {
	for _, r := range value {
		return string(r), true
	}
	return "", false
}
