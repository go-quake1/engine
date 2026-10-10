module github.com/go-quake1/engine

go 1.27.1

require (
	github.com/go-virtio/common v0.3.0
	github.com/go-virtio/gpu v0.8.0
	github.com/go-virtio/input v0.0.0
	github.com/go-virtio/sound v0.0.0
	github.com/go-virtio/validate v0.1.0
	github.com/jfreymuth/oggvorbis v1.0.5
	github.com/usbarmory/tamago v1.27.2
)

require github.com/jfreymuth/vorbis v1.0.2 // indirect

replace (
	github.com/go-virtio/common => ../../go-virtio/common
	github.com/go-virtio/gpu => ../../go-virtio/gpu
	github.com/go-virtio/input => ../../go-virtio/input
	github.com/go-virtio/sound => ../../go-virtio/sound
	github.com/go-virtio/validate => ../../go-virtio/validate
	github.com/usbarmory/tamago => ../../usbarmory/tamago
)
