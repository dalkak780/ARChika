//go:build !vips

package app

func newImageProcessor() ImageProcessor { return GoProcessor{} }
