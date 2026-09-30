// Package store encodes the display's persistent settings into a fixed
// record for the two 4 KB flash sectors (A/B). Fixed offsets instead of a
// serialisation library: the record is 271 bytes, the decoder must not
// allocate, and a format change is a version bump, not a schema migration.
//
// Header: magic "GCS1" | version u8 | generation u32 LE | length u16 LE |
// crc32 (IEEE, over the payload) u32 LE. Payload: PayloadSize bytes at the
// offsets below; unused bytes are zero.
package store

import (
	"encoding/binary"
	"hash/crc32"

	"claudecontrol/firmware/internal/touchcal"
)

const (
	SectorSize  = 4096
	PayloadSize = 256
	headerSize  = 4 + 1 + 4 + 2 + 4
	RecordSize  = headerSize + PayloadSize

	version = 1

	// Payload offsets.
	offSSIDLen      = 0
	offSSID         = 1   // 32 bytes
	offPasswordLen  = 33
	offPassword     = 34  // 64 bytes
	offWiFiVerified = 98
	offWiFiAttempts = 99
	offHasIdentity  = 100
	offDeviceID     = 101 // 16 bytes
	offSecret       = 117 // 32 bytes
	offPaired       = 149
	offAgentAddrs   = 150 // 3 × 4 bytes
	offAgentPort    = 162 // u16
	offAgentID      = 164 // 16 bytes
	offCalValid     = 180
	offCalSwap      = 181
	offCalX0        = 182 // 4 × int16
	offSoundOff     = 190

	SSIDMax     = 32
	PasswordMax = 64
	agentAddrs  = 3
)

var magic = [4]byte{'G', 'C', 'S', '1'}

// Record is everything the display remembers across power cycles. Stage A1
// uses Cal and SoundOff; the WiFi, identity and agent fields belong to the
// later stages but are laid out now so the format does not change.
type Record struct {
	SSID         [SSIDMax]byte
	SSIDLen      uint8
	Password     [PasswordMax]byte
	PasswordLen  uint8
	WiFiVerified bool
	WiFiAttempts uint8

	DeviceID    [16]byte
	Secret      [32]byte
	HasIdentity bool

	AgentAddrs [agentAddrs][4]byte
	AgentPort  uint16
	AgentID    [16]byte
	Paired     bool

	Cal      touchcal.Cal
	SoundOff bool
}

// Encode serialises the record with the given generation number.
func Encode(r Record, generation uint32) [RecordSize]byte {
	var b [RecordSize]byte
	p := b[headerSize:]

	p[offSSIDLen] = clampLen(r.SSIDLen, SSIDMax)
	copy(p[offSSID:offSSID+SSIDMax], r.SSID[:])
	p[offPasswordLen] = clampLen(r.PasswordLen, PasswordMax)
	copy(p[offPassword:offPassword+PasswordMax], r.Password[:])
	p[offWiFiVerified] = boolByte(r.WiFiVerified)
	p[offWiFiAttempts] = r.WiFiAttempts
	p[offHasIdentity] = boolByte(r.HasIdentity)
	copy(p[offDeviceID:offDeviceID+len(r.DeviceID)], r.DeviceID[:])
	copy(p[offSecret:offSecret+len(r.Secret)], r.Secret[:])
	p[offPaired] = boolByte(r.Paired)
	for i := range r.AgentAddrs {
		copy(p[offAgentAddrs+4*i:offAgentAddrs+4*i+4], r.AgentAddrs[i][:])
	}
	binary.LittleEndian.PutUint16(p[offAgentPort:], r.AgentPort)
	copy(p[offAgentID:offAgentID+len(r.AgentID)], r.AgentID[:])
	p[offCalValid] = boolByte(r.Cal.Valid)
	p[offCalSwap] = boolByte(r.Cal.Swap)
	binary.LittleEndian.PutUint16(p[offCalX0:], uint16(r.Cal.X0))
	binary.LittleEndian.PutUint16(p[offCalX0+2:], uint16(r.Cal.X1))
	binary.LittleEndian.PutUint16(p[offCalX0+4:], uint16(r.Cal.Y0))
	binary.LittleEndian.PutUint16(p[offCalX0+6:], uint16(r.Cal.Y1))
	p[offSoundOff] = boolByte(r.SoundOff)

	copy(b[0:4], magic[:])
	b[4] = version
	binary.LittleEndian.PutUint32(b[5:], generation)
	binary.LittleEndian.PutUint16(b[9:], PayloadSize)
	binary.LittleEndian.PutUint32(b[11:], crc32.ChecksumIEEE(p))

	return b
}

// Decode parses a record from the start of a sector image. ok is false for
// erased flash, a foreign magic, another version, a short buffer or a CRC
// mismatch.
func Decode(b []byte) (Record, uint32, bool) {
	var r Record
	if len(b) < RecordSize || [4]byte(b[0:4]) != magic || b[4] != version {
		return r, 0, false
	}
	if binary.LittleEndian.Uint16(b[9:]) != PayloadSize {
		return r, 0, false
	}
	p := b[headerSize:RecordSize]
	if crc32.ChecksumIEEE(p) != binary.LittleEndian.Uint32(b[11:]) {
		return r, 0, false
	}

	r.SSIDLen = clampLen(p[offSSIDLen], SSIDMax)
	copy(r.SSID[:], p[offSSID:offSSID+SSIDMax])
	r.PasswordLen = clampLen(p[offPasswordLen], PasswordMax)
	copy(r.Password[:], p[offPassword:offPassword+PasswordMax])
	r.WiFiVerified = p[offWiFiVerified] == 1
	r.WiFiAttempts = p[offWiFiAttempts]
	r.HasIdentity = p[offHasIdentity] == 1
	copy(r.DeviceID[:], p[offDeviceID:offDeviceID+len(r.DeviceID)])
	copy(r.Secret[:], p[offSecret:offSecret+len(r.Secret)])
	r.Paired = p[offPaired] == 1
	for i := range r.AgentAddrs {
		copy(r.AgentAddrs[i][:], p[offAgentAddrs+4*i:offAgentAddrs+4*i+4])
	}
	r.AgentPort = binary.LittleEndian.Uint16(p[offAgentPort:])
	copy(r.AgentID[:], p[offAgentID:offAgentID+len(r.AgentID)])
	r.Cal = touchcal.Cal{
		Valid: p[offCalValid] == 1,
		Swap:  p[offCalSwap] == 1,
		X0:    int16(binary.LittleEndian.Uint16(p[offCalX0:])),
		X1:    int16(binary.LittleEndian.Uint16(p[offCalX0+2:])),
		Y0:    int16(binary.LittleEndian.Uint16(p[offCalX0+4:])),
		Y1:    int16(binary.LittleEndian.Uint16(p[offCalX0+6:])),
	}
	r.SoundOff = p[offSoundOff] == 1

	return r, binary.LittleEndian.Uint32(b[5:]), true
}

// Newest returns the valid record with the higher generation from the two
// sector images; on a tie slot B wins (the writer alternates slots, so an
// equal generation means B was written to replace A and A's erase never
// happened). slot is -1 when neither is valid.
func Newest(a, b []byte) (Record, int, uint32, bool) {
	ra, ga, oka := Decode(a)
	rb, gb, okb := Decode(b)

	switch {
	case oka && okb && ga > gb:
		return ra, 0, ga, true
	case okb:
		return rb, 1, gb, true
	case oka:
		return ra, 0, ga, true
	}

	return Record{}, -1, 0, false
}

// NextSlot is the slot the next write should overwrite: a broken one first,
// else the older one, so the newest record always survives a failed write.
func NextSlot(a, b []byte) int {
	_, ga, oka := Decode(a)
	_, gb, okb := Decode(b)

	switch {
	case !oka:
		return 0
	case !okb:
		return 1
	case ga <= gb:
		return 0
	}

	return 1
}

func clampLen(n, limit uint8) uint8 {
	if n > limit {
		return limit
	}

	return n
}

func boolByte(v bool) byte {
	if v {
		return 1
	}

	return 0
}
