//go:build esp32

package main

/*
#include <stdint.h>

// ESP32 ROM routines at their fixed addresses (esp32.rom*.ld). They drive the
// SPI1 flash controller directly, so the cache must be off and the caller
// must run from IRAM while they work; the ROM's own "unlock" is not used
// (ESP-IDF masks its address on purpose).
typedef int  (*rom_erase_t)(uint32_t sector);
typedef int  (*rom_write_t)(uint32_t dest, const uint32_t *src, uint32_t len);
typedef int  (*rom_read_t)(uint32_t src, uint32_t *dst, uint32_t len);
typedef void (*rom_cache_t)(int cpu);
typedef void (*rom_cfg_t)(uint32_t id, uint32_t chip, uint32_t block, uint32_t sector, uint32_t page, uint32_t mask);

#define ROM_ERASE_SECTOR       ((rom_erase_t)0x40062ccc)
#define ROM_WRITE              ((rom_write_t)0x40062d50)
#define ROM_READ               ((rom_read_t)0x40062ed8)
#define ROM_CACHE_READ_DISABLE ((rom_cache_t)0x40009ab8)
#define ROM_CACHE_READ_ENABLE  ((rom_cache_t)0x40009a84)
#define ROM_CACHE_FLUSH        ((rom_cache_t)0x40009a14)
#define ROM_CONFIG_PARAM       ((rom_cfg_t)0x40063238)

#define SECTOR_SIZE 4096

static inline uint32_t irq_off(void) {
	uint32_t ps;
	__asm__ volatile("rsil %0, 15" : "=a"(ps));
	return ps;
}

static inline void irq_restore(uint32_t ps) {
	__asm__ volatile("wsr.ps %0; rsync" :: "a"(ps));
}

// The ROM loader sizes the chip from the image header, which TinyGo writes
// as 2 MB; declare the real 4 MB so addresses above pass its range check.
static void gc_flash_declare_4mb(void) {
	ROM_CONFIG_PARAM(0, 4*1024*1024, 64*1024, SECTOR_SIZE, 256, 0xffff);
}

__attribute__((section(".iram1.gc_flash_program"), noinline))
int gc_flash_program(uint32_t addr, const uint32_t *data, uint32_t len) {
	uint32_t ps = irq_off();
	ROM_CACHE_READ_DISABLE(0);
	int rc = ROM_ERASE_SECTOR(addr / SECTOR_SIZE);
	if (rc == 0) rc = ROM_WRITE(addr, data, len);
	ROM_CACHE_FLUSH(0);
	ROM_CACHE_READ_ENABLE(0);
	irq_restore(ps);
	return rc;
}

__attribute__((section(".iram1.gc_flash_read"), noinline))
int gc_flash_read(uint32_t addr, uint32_t *data, uint32_t len) {
	uint32_t ps = irq_off();
	ROM_CACHE_READ_DISABLE(0);
	int rc = ROM_READ(addr, data, len);
	ROM_CACHE_FLUSH(0);
	ROM_CACHE_READ_ENABLE(0);
	irq_restore(ps);
	return rc;
}
*/
import "C"

import (
	"unsafe"

	"claudecontrol/firmware/internal/store"
)

// Two sectors at the top of the 4 MB flash hold the settings record (A/B);
// the image and its mapped segments live far below.
const (
	storeSlotA = 0x3F0000
	storeSlotB = 0x3F1000

	// The ROM works on 32-bit words; the record is padded up to whole words.
	storeWords = (store.RecordSize + 3) / 4
)

var (
	// Word-aligned images of both slots, as the ROM read/write want them.
	storeBufA [storeWords]uint32
	storeBufB [storeWords]uint32

	// settingsRecord caches the current record so a preference change does
	// not re-read flash, and the display can grow the record in later
	// stages without re-reading here.
	settingsRecord   store.Record
	settingsGen      uint32
	settingsDeclared bool
)

func wordsToBytes(w *[storeWords]uint32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&w[0])), storeWords*4)
}

// readStore loads the newest valid record from flash (an empty Record when
// both slots are blank or broken) and remembers it.
func readStore() store.Record {
	if !settingsDeclared {
		C.gc_flash_declare_4mb()
		settingsDeclared = true
	}

	C.gc_flash_read(C.uint32_t(storeSlotA), (*C.uint32_t)(unsafe.Pointer(&storeBufA[0])), C.uint32_t(storeWords*4))
	C.gc_flash_read(C.uint32_t(storeSlotB), (*C.uint32_t)(unsafe.Pointer(&storeBufB[0])), C.uint32_t(storeWords*4))

	rec, _, gen, ok := store.Newest(wordsToBytes(&storeBufA), wordsToBytes(&storeBufB))
	if !ok {
		rec = store.Record{}
		gen = 0
	}

	settingsRecord = rec
	settingsGen = gen

	return rec
}

// writeStore writes the record into the slot that holds the older (or no)
// record, with the next generation. Erase keeps interrupts off for ~40 ms,
// so callers do it between frames. A failure is logged and ignored: losing
// a preference beats bricking the loop.
func writeStore(rec store.Record) {
	if !settingsDeclared {
		readStore()
	}

	feedWatchdog()

	slot := store.NextSlot(wordsToBytes(&storeBufA), wordsToBytes(&storeBufB))
	settingsGen++
	encoded := store.Encode(rec, settingsGen)

	buf, addr := &storeBufA, uint32(storeSlotA)
	if slot == 1 {
		buf, addr = &storeBufB, storeSlotB
	}
	copy(wordsToBytes(buf), encoded[:])

	if rc := C.gc_flash_program(C.uint32_t(addr), (*C.uint32_t)(unsafe.Pointer(&buf[0])), C.uint32_t(storeWords*4)); rc != 0 {
		println("settings write failed, rom rc", int(rc))

		return
	}

	settingsRecord = rec
}

// settings is the core's view of the preferences; the display keeps more in
// the same record.
type settings struct {
	soundOff bool
}

func loadSettings() settings {
	return settings{soundOff: readStore().SoundOff}
}

// saveSettings merges the core's preference into the full record so a sound
// toggle never wipes the calibration or the network.
func saveSettings(s settings) {
	rec := settingsRecord
	rec.SoundOff = s.soundOff
	writeStore(rec)
}
