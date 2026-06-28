module github.com/go-quake1/engine

go 1.26.4

require (
	github.com/go-virtio/common v0.1.6
	github.com/go-virtio/gpu v0.5.0
	github.com/go-virtio/input v0.0.0
	github.com/go-virtio/sound v0.0.0
	github.com/go-virtio/validate v0.1.0
	github.com/jfreymuth/oggvorbis v1.0.5
	github.com/usbarmory/tamago v1.26.4
)

require github.com/jfreymuth/vorbis v1.0.2 // indirect

replace (
	github.com/go-virtio/common => ../../go-virtio/common
	github.com/go-virtio/gpu => ../../go-virtio/gpu
	github.com/go-virtio/input => ../../go-virtio/input
	github.com/go-virtio/sound => ../../go-virtio/sound
	github.com/go-virtio/validate => ../../go-virtio/validate
	github.com/jfreymuth/oggvorbis => ../../jfreymuth/oggvorbis
	github.com/jfreymuth/vorbis => ../../jfreymuth/vorbis
	github.com/usbarmory/tamago => ../../usbarmory/tamago
)
