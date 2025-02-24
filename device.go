package evdev

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"
)

var eventsize = int(unsafe.Sizeof(InputEvent{}))

// InputDevice represent a Linux kernel input device in userspace.
// It can be used to query and write device properties, read input events,
// or grab it for exclusive access.
type InputDevice struct {
	file          *os.File
	driverVersion int32
}

// Open creates a new InputDevice from the given path. Returns an error if
// the device node could not be opened or its properties failed to read.
func Open(path string) (*InputDevice, error) {
	d := &InputDevice{}

	var err error
	d.file, err = os.Open(path)
	if err != nil {
		return nil, err
	}

	d.driverVersion, err = ioctlEVIOCGVERSION(d.file.Fd())
	if err != nil {
		return nil, fmt.Errorf("Cannot get driver version: %v", err)
	}

	return d, nil
}

// Close releases the resources held by an InputDevice. After calling this
// function, the InputDevice is no longer operational.
func (d *InputDevice) Close() {
	d.file.Close()
}

// Path returns the device's node path it was opened under.
func (d *InputDevice) Path() string {
	return d.file.Name()
}

// DriverVersion returns the version of the Linux Evdev driver.
// The three ints returned by this function describe the major, minor and
// micro parts of the version code.
func (d *InputDevice) DriverVersion() (int, int, int) {
	return int((d.driverVersion >> 16)),
		int((d.driverVersion >> 8) & 0xff),
		int((d.driverVersion >> 0) & 0xff)
}

// Name returns the device's name as reported by the kernel.
func (d *InputDevice) Name() (string, error) {
	return ioctlEVIOCGNAME(d.file.Fd())
}

// PhysicalLocation returns the device's physical location as reported by the kernel.
func (d *InputDevice) PhysicalLocation() (string, error) {
	return ioctlEVIOCGPHYS(d.file.Fd())
}

// UniqueID returns the device's unique identifier as reported by the kernel.
func (d *InputDevice) UniqueID() (string, error) {
	return ioctlEVIOCGUNIQ(d.file.Fd())
}

// InputID returns the device's vendor/product/busType/version information as reported by the kernel.
func (d *InputDevice) InputID() (InputID, error) {
	return ioctlEVIOCGID(d.file.Fd())
}

// CapableTypes returns a slice of EvType that are the device supports
func (d *InputDevice) CapableTypes() []EvType {
	types := []EvType{}

	evBits, err := ioctlEVIOCGBIT(d.file.Fd(), 0)
	if err != nil {
		return []EvType{}
	}

	evBitmap := newBitmap(evBits)

	for _, t := range evBitmap.setBits() {
		types = append(types, EvType(t))
	}

	return types
}

// Properties returns a slice of EvProp that are the device supports
func (d *InputDevice) Properties() []EvProp {
	props := []EvProp{}

	propBits, err := ioctlEVIOCGPROP(d.file.Fd())
	if err != nil {
		return []EvProp{}
	}

	propBitmap := newBitmap(propBits)

	for _, p := range propBitmap.setBits() {
		props = append(props, EvProp(p))
	}

	return props
}

// State return a StateMap for the given type. The map will be empty if the requested type
// is not supported by the device.
func (d *InputDevice) State(t EvType) (StateMap, error) {
	fd := d.file.Fd()

	evBits, err := ioctlEVIOCGBIT(fd, 0)
	if err != nil {
		return nil, fmt.Errorf("Cannot get evBits: %v", err)
	}

	evBitmap := newBitmap(evBits)

	if !evBitmap.bitIsSet(int(t)) {
		return StateMap{}, nil
	}

	codeBits, err := ioctlEVIOCGBIT(fd, int(t))
	if err != nil {
		return nil, fmt.Errorf("Cannot get evBits: %v", err)
	}

	codeBitmap := newBitmap(codeBits)

	stateBits := []byte{}

	switch t {
	case EV_KEY:
		stateBits, err = ioctlEVIOCGKEY(fd)
	case EV_SW:
		stateBits, err = ioctlEVIOCGSW(fd)
	case EV_LED:
		stateBits, err = ioctlEVIOCGLED(fd)
	case EV_SND:
		stateBits, err = ioctlEVIOCGSND(fd)
	default:
		err = fmt.Errorf("Unsupported evType %d", t)
	}

	if err != nil {
		return nil, err
	}

	stateBitmap := newBitmap(stateBits)
	st := StateMap{}

	for _, code := range codeBitmap.setBits() {
		st[EvCode(code)] = stateBitmap.bitIsSet(code)
	}

	return st, nil
}

// AbsInfos returns the AbsInfo struct for all axis the device supports.
func (d *InputDevice) AbsInfos() (map[EvCode]AbsInfo, error) {
	a := make(map[EvCode]AbsInfo)

	absBits, err := ioctlEVIOCGBIT(d.file.Fd(), EV_ABS)
	if err != nil {
		return nil, fmt.Errorf("Cannot get absBits: %v", err)
	}

	absBitmap := newBitmap(absBits)

	for _, abs := range absBitmap.setBits() {
		absInfo, err := ioctlEVIOCGABS(d.file.Fd(), abs)
		if err == nil {
			a[EvCode(abs)] = absInfo
		}
	}

	return a, nil
}

// Grab grabs the device for exclusive access. No other process will receive
// input events until the device instance is active.
func (d *InputDevice) Grab() error {
	return ioctlEVIOCGRAB(d.file.Fd())
}

// Revoke releases a previously taken exclusive use with Grab().
func (d *InputDevice) Revoke() error {
	return ioctlEVIOCREVOKE(d.file.Fd())
}

// Read and return a slice of input events from device.
func (d *InputDevice) Read() ([]InputEvent, error) {
	events := make([]InputEvent, 16)
	buffer := make([]byte, eventsize*16)

	_, err := d.file.Read(buffer)
	if err != nil {
		return events, err
	}

	b := bytes.NewBuffer(buffer)
	err = binary.Read(b, binary.LittleEndian, &events)
	if err != nil {
		return events, err
	}

	// remove trailing structures
	for i := range events {
		if events[i].Time.Sec == 0 {
			events = append(events[:i])
			break
		}
	}

	return events, err
}

// ReadOne reads one InputEvent from the device. It blocks until an event has
// been received or an error has occured.
func (d *InputDevice) ReadOne() (*InputEvent, error) {
	event := InputEvent{}
	buffer := make([]byte, eventsize)

	_, err := d.file.Read(buffer)
	if err != nil {
		return &event, err
	}

	b := bytes.NewBuffer(buffer)
	err = binary.Read(b, binary.LittleEndian, &event)
	if err != nil {
		return nil, err
	}

	return &event, nil
}
