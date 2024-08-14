package packager

import "errors"

var (
	ErrMissingDevice                 = errors.New("missing resource")
	ErrCouldNotOpenPackageOutputFile = errors.New("could not open package output file")
	ErrCouldNotCreateCompressor      = errors.New("could not create compressor")
	ErrCouldNotStatDevice            = errors.New("could not stat device")
	ErrCouldNotCreateTarHeader       = errors.New("could not create tar header")
	ErrCouldNotWriteTarHeader        = errors.New("could not write tar header")
	ErrCouldNotOpenDevice            = errors.New("could not open device file")
	ErrCouldNotCopyToArchive         = errors.New("could not copy file to archive")
	ErrCouldNotOpenPackageInputFile  = errors.New("could not open package input file")
	ErrCouldNotCreateUncompressor    = errors.New("could not create uncompressor")
	ErrCouldNotReadNextHeader        = errors.New("could not read next header from archive")
	ErrCouldNotCreateOutputDir       = errors.New("could not create output directory")
	ErrCouldNotOpenOutputFile        = errors.New("could not open output file")
	ErrCouldNotCopyToOutput          = errors.New("could not copy file to output")
)
