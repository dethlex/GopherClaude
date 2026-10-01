//go:build esp32

package main

import (
	"device/esp"
	"machine"
	"runtime/volatile"
	"unsafe"
)

// TinyGo has no PWM, watchdog or reset support for the classic ESP32, but
// the register map is there. Three small helpers cover what the display
// needs: LEDC (backlight, tone, LED dimming), the TIMG0 watchdog and a
// software reset.
const (
	ledcAPBHz    = 80_000_000
	ledcDutyBits = 10
	ledcDutyMax  = 1<<ledcDutyBits - 1
	ledcDivFrac  = 8  // DIV_NUM is a 10.8 fixed-point divider
	ledcHSSig0   = 71 // GPIO-matrix signal LEDC_HS_SIG_OUT0; channel n is +n
	ledcTickAPB  = 1  // TICK_SEL: APB clock

	ledcTimerBacklight = 0
	ledcTimerTone      = 1
	ledcTimerLED       = 2

	ledcChBacklight = 0
	ledcChTone      = 1
	ledcChLEDR      = 2
	ledcChLEDG      = 3
	ledcChLEDB      = 4

	backlightHz = 5000
	ledHz       = 1000

	// GPIO matrix: FUNCn_OUT_SEL_CFG registers sit 4 bytes apart from FUNC0.
	gpioOutSelStride = 4

	// TIMG watchdog: 80 MHz / prescale = 2 kHz, i.e. 0.5 ms per tick.
	wdtKey         = 0x50D83AA1
	wdtPrescale    = 40000
	wdtTicksPerMs  = 2
	wdtStageReset  = 3 // stage action: system reset
	wdtResetLength = 7 // longest reset pulse
	wdtStageOff    = 0
)

func ledcSetup() {
	esp.DPORT.SetPERIP_CLK_EN_LEDC_CLK_EN(1)
	esp.DPORT.SetPERIP_RST_EN_LEDC_RST(0)
	esp.LEDC.SetCONF_APB_CLK_SEL(1)

	ledcSetFrequency(ledcTimerBacklight, backlightHz)
	ledcSetFrequency(ledcTimerLED, ledHz)
	ledcSetFrequency(ledcTimerTone, beepFreqHigh)
}

// ledcSetFrequency programs a high-speed timer for hz at ledcDutyBits of
// resolution: divider = APB * 2^8 / (hz * 2^bits), in 10.8 fixed point.
func ledcSetFrequency(timer uint8, hz uint32) {
	div := uint32((uint64(ledcAPBHz) << ledcDivFrac) / (uint64(hz) << ledcDutyBits))

	switch timer {
	case 0:
		esp.LEDC.SetHSTIMER0_CONF_TICK_SEL(ledcTickAPB)
		esp.LEDC.SetHSTIMER0_CONF_DUTY_RES(ledcDutyBits)
		esp.LEDC.SetHSTIMER0_CONF_DIV_NUM(div)
		esp.LEDC.SetHSTIMER0_CONF_RST(1)
		esp.LEDC.SetHSTIMER0_CONF_RST(0)
	case 1:
		esp.LEDC.SetHSTIMER1_CONF_TICK_SEL(ledcTickAPB)
		esp.LEDC.SetHSTIMER1_CONF_DUTY_RES(ledcDutyBits)
		esp.LEDC.SetHSTIMER1_CONF_DIV_NUM(div)
		esp.LEDC.SetHSTIMER1_CONF_RST(1)
		esp.LEDC.SetHSTIMER1_CONF_RST(0)
	case 2:
		esp.LEDC.SetHSTIMER2_CONF_TICK_SEL(ledcTickAPB)
		esp.LEDC.SetHSTIMER2_CONF_DUTY_RES(ledcDutyBits)
		esp.LEDC.SetHSTIMER2_CONF_DIV_NUM(div)
		esp.LEDC.SetHSTIMER2_CONF_RST(1)
		esp.LEDC.SetHSTIMER2_CONF_RST(0)
	}
}

// ledcAttach binds a channel to a timer and routes it to a pin through the
// GPIO matrix. Pin.Configure sets the pad up as a plain output first (mux,
// output enable); overwriting its output-select afterwards swaps the
// "GPIO register" source for the LEDC signal.
func ledcAttach(ch, timer uint8, pin machine.Pin) {
	pin.Configure(machine.PinConfig{Mode: machine.PinOutput})

	outSel := (*volatile.Register32)(unsafe.Add(unsafe.Pointer(&esp.GPIO.FUNC0_OUT_SEL_CFG), uintptr(pin)*gpioOutSelStride))
	outSel.Set(ledcHSSig0 + uint32(ch))

	switch ch {
	case 0:
		esp.LEDC.SetHSCH0_CONF0_TIMER_SEL(uint32(timer))
		esp.LEDC.SetHSCH0_CONF0_SIG_OUT_EN(1)
	case 1:
		esp.LEDC.SetHSCH1_CONF0_TIMER_SEL(uint32(timer))
		esp.LEDC.SetHSCH1_CONF0_SIG_OUT_EN(1)
	case 2:
		esp.LEDC.SetHSCH2_CONF0_TIMER_SEL(uint32(timer))
		esp.LEDC.SetHSCH2_CONF0_SIG_OUT_EN(1)
	case 3:
		esp.LEDC.SetHSCH3_CONF0_TIMER_SEL(uint32(timer))
		esp.LEDC.SetHSCH3_CONF0_SIG_OUT_EN(1)
	case 4:
		esp.LEDC.SetHSCH4_CONF0_TIMER_SEL(uint32(timer))
		esp.LEDC.SetHSCH4_CONF0_SIG_OUT_EN(1)
	}

	ledcSetDuty(ch, 0)
}

// ledcSetDuty sets a channel's duty (0..ledcDutyMax) and latches it; the
// DUTY register keeps four fractional bits below the integer duty.
func ledcSetDuty(ch uint8, duty uint32) {
	if duty > ledcDutyMax {
		duty = ledcDutyMax
	}

	switch ch {
	case 0:
		esp.LEDC.SetHSCH0_DUTY_DUTY(duty << 4)
		esp.LEDC.SetHSCH0_CONF1_DUTY_START(1)
	case 1:
		esp.LEDC.SetHSCH1_DUTY_DUTY(duty << 4)
		esp.LEDC.SetHSCH1_CONF1_DUTY_START(1)
	case 2:
		esp.LEDC.SetHSCH2_DUTY_DUTY(duty << 4)
		esp.LEDC.SetHSCH2_CONF1_DUTY_START(1)
	case 3:
		esp.LEDC.SetHSCH3_DUTY_DUTY(duty << 4)
		esp.LEDC.SetHSCH3_CONF1_DUTY_START(1)
	case 4:
		esp.LEDC.SetHSCH4_DUTY_DUTY(duty << 4)
		esp.LEDC.SetHSCH4_CONF1_DUTY_START(1)
	}
}

// startWatchdog arms the TIMG0 watchdog to reset the chip when the main loop
// stalls for watchdogTimeoutMillis.
func startWatchdog() {
	t := esp.TIMG0
	t.WDTWPROTECT.Set(wdtKey)
	t.SetWDTCONFIG0_WDT_EN(0)
	t.SetWDTCONFIG1_WDT_CLK_PRESCALE(wdtPrescale)
	t.WDTCONFIG2.Set(watchdogTimeoutMillis * wdtTicksPerMs)
	t.SetWDTCONFIG0_WDT_STG0(wdtStageReset)
	t.SetWDTCONFIG0_WDT_STG1(wdtStageOff)
	t.SetWDTCONFIG0_WDT_STG2(wdtStageOff)
	t.SetWDTCONFIG0_WDT_STG3(wdtStageOff)
	t.SetWDTCONFIG0_WDT_SYS_RESET_LENGTH(wdtResetLength)
	t.SetWDTCONFIG0_WDT_CPU_RESET_LENGTH(wdtResetLength)
	t.SetWDTCONFIG0_WDT_EN(1)
	t.WDTWPROTECT.Set(0)
}

func feedWatchdog() {
	esp.TIMG0.WDTWPROTECT.Set(wdtKey)
	esp.TIMG0.WDTFEED.Set(1)
	esp.TIMG0.WDTWPROTECT.Set(0)
}

// softReset restarts the chip as if EN had been pulsed. Used by the later
// stages (a WiFi association the access point refuses is cured only by a
// fresh boot); it never returns.
func softReset() {
	esp.RTC_CNTL.SetOPTIONS0_SW_SYS_RST(1)

	for {
	}
}
