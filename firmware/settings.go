package main

import "machine"

// Persistent settings live in the first erase block of the writable flash
// area (right after the program). Only user-chosen preferences are stored;
// host data and ephemeral UI state (current page, cursor row) are not.
//
// On-flash record, padded to the write block size with 0xFF:
//
//	magic(4, big-endian) | version(1) | soundOff(1)
const (
	settingsMagic   = 0x43435331 // "CCS1"
	settingsVersion = 1

	settingsOffset = 0 // first free erase block
	settingsRecLen = 6
)

type settings struct {
	soundOff bool
}

// loadSettings reads settings from flash, returning defaults when the region
// is unwritten (erased flash reads as 0xFF) or holds a foreign/older record.
func loadSettings() settings {
	var buf [settingsRecLen]byte

	if _, err := machine.Flash.ReadAt(buf[:], settingsOffset); err != nil {
		return settings{}
	}

	magic := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
	if magic != settingsMagic || buf[4] != settingsVersion {
		return settings{}
	}

	return settings{soundOff: buf[5] == 1}
}

// saveSettings erases the block and writes the record. A failure is logged
// and ignored — losing a preference across reboots beats bricking the loop.
func saveSettings(s settings) {
	// Feed the watchdog first: erase+write runs with interrupts disabled,
	// though it completes in a few milliseconds, well under the timeout.
	machine.Watchdog.Update()

	if err := machine.Flash.EraseBlocks(settingsOffset, 1); err != nil {
		println("settings erase:", err.Error())

		return
	}

	var buf [settingsRecLen]byte

	buf[0] = byte((settingsMagic >> 24) & 0xFF)
	buf[1] = byte((settingsMagic >> 16) & 0xFF)
	buf[2] = byte((settingsMagic >> 8) & 0xFF)
	buf[3] = byte(settingsMagic & 0xFF)
	buf[4] = settingsVersion
	buf[5] = boolToByte(s.soundOff)

	if _, err := machine.Flash.WriteAt(buf[:], settingsOffset); err != nil {
		println("settings write:", err.Error())
	}
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}

	return 0
}
