package main

import (
	"machine"

	"tinygo.org/x/drivers/lis3dh"
)

// Rest detection: on this board the LIS3DH Z axis points out the BACK of the
// badge, so lying flat screen-UP reads ≈ -1g (verified on hardware). That is
// the badge's "do not disturb" gesture: put it down on the desk the natural
// way and it goes quiet; pick it up (or wear it — hanging gives Z ≈ 0) and it
// wakes. The threshold is well past 45° so a lanyard never triggers it.
const restThresholdMicroG = -700_000

var (
	accel   lis3dh.Device
	accelOK bool
)

func initAccel() {
	machine.I2C0.Configure(machine.I2CConfig{
		SDA: machine.I2C0_SDA_PIN,
		SCL: machine.I2C0_SCL_PIN,
	})

	accel = lis3dh.New(machine.I2C0)

	if err := accel.Configure(lis3dh.Config{Address: lis3dh.Address0}); err != nil {
		println("accel configure:", err.Error())

		return
	}

	if !accel.Connected() {
		println("accel not connected")

		return
	}

	accelOK = true
}

var (
	prevAccel      [3]int32
	prevAccelValid bool
)

// readAccel samples the accelerometer once per call and returns the Z axis
// (for rest detection) plus the total change since the previous sample (for
// motion wake-up). ok is false when the sensor is absent or misreads — the
// caller then behaves as if the badge were held still and upright.
func readAccel() (z, delta int32, ok bool) {
	if !accelOK {
		return 0, 0, false
	}

	x, y, z, err := accel.ReadAcceleration()
	if err != nil {
		return 0, 0, false
	}

	if prevAccelValid {
		delta = absInt32(x-prevAccel[0]) + absInt32(y-prevAccel[1]) + absInt32(z-prevAccel[2])
	}

	prevAccel = [3]int32{x, y, z}
	prevAccelValid = true

	return z, delta, true
}

func absInt32(v int32) int32 {
	if v < 0 {
		return -v
	}

	return v
}
