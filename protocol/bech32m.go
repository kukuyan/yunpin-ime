// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"errors"
	"fmt"
	"strings"
)

const (
	recoveryHRP     = "yprec"
	bech32mConstant = uint32(0x2bc830a3)
)

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func bech32Polymod(values []byte) uint32 {
	checksum := uint32(1)
	generators := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	for _, value := range values {
		top := checksum >> 25
		checksum = (checksum&0x1ffffff)<<5 ^ uint32(value)
		for index, generator := range generators {
			if (top>>index)&1 == 1 {
				checksum ^= generator
			}
		}
	}
	return checksum
}

func expandHRP(hrp string) []byte {
	values := make([]byte, 0, len(hrp)*2+1)
	for _, value := range []byte(hrp) {
		values = append(values, value>>5)
	}
	values = append(values, 0)
	for _, value := range []byte(hrp) {
		values = append(values, value&31)
	}
	return values
}

func convertBits(input []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	var accumulator uint32
	var bitCount uint
	maxValue := uint32(1<<toBits) - 1
	maxAccumulator := uint32(1<<(fromBits+toBits-1)) - 1
	output := make([]byte, 0, len(input)*int(fromBits)/int(toBits)+1)
	for _, value := range input {
		if uint32(value)>>fromBits != 0 {
			return nil, errors.New("input value exceeds source bit width")
		}
		accumulator = ((accumulator << fromBits) | uint32(value)) & maxAccumulator
		bitCount += fromBits
		for bitCount >= toBits {
			bitCount -= toBits
			output = append(output, byte((accumulator>>bitCount)&maxValue))
		}
	}
	if pad {
		if bitCount > 0 {
			output = append(output, byte((accumulator<<(toBits-bitCount))&maxValue))
		}
	} else if bitCount >= fromBits || ((accumulator<<(toBits-bitCount))&maxValue) != 0 {
		return nil, errors.New("invalid zero padding")
	}
	return output, nil
}

func EncodeRecoveryKey(key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("recovery key must be 32 bytes")
	}
	data, err := convertBits(key, 8, 5, true)
	if err != nil {
		return "", err
	}
	values := append(expandHRP(recoveryHRP), data...)
	values = append(values, make([]byte, 6)...)
	polymod := bech32Polymod(values) ^ bech32mConstant
	checksum := make([]byte, 6)
	for index := range checksum {
		checksum[index] = byte((polymod >> (5 * (5 - index))) & 31)
	}
	encoded := make([]byte, 0, len(data)+6)
	for _, value := range append(data, checksum...) {
		encoded = append(encoded, bech32Charset[value])
	}
	return recoveryHRP + "1" + string(encoded), nil
}

func DecodeRecoveryKey(encoded string) ([]byte, error) {
	if encoded != strings.ToLower(encoded) && encoded != strings.ToUpper(encoded) {
		return nil, errors.New("mixed-case recovery key")
	}
	encoded = strings.ToLower(encoded)
	separator := strings.LastIndexByte(encoded, '1')
	if separator <= 0 || separator+7 > len(encoded) {
		return nil, errors.New("invalid recovery key format")
	}
	if encoded[:separator] != recoveryHRP {
		return nil, fmt.Errorf("unexpected recovery key prefix %q", encoded[:separator])
	}
	values := make([]byte, 0, len(encoded)-separator-1)
	for _, character := range []byte(encoded[separator+1:]) {
		index := strings.IndexByte(bech32Charset, character)
		if index < 0 {
			return nil, errors.New("invalid recovery key character")
		}
		values = append(values, byte(index))
	}
	checkValues := append(expandHRP(recoveryHRP), values...)
	if bech32Polymod(checkValues) != bech32mConstant {
		return nil, errors.New("recovery key checksum mismatch")
	}
	decoded, err := convertBits(values[:len(values)-6], 5, 8, false)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 32 {
		return nil, errors.New("recovery key payload must be 32 bytes")
	}
	return decoded, nil
}
