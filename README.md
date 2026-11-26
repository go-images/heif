# heif

A pure-Go reader for HEIF / HEIC still images (ISO/IEC 23008-12 on the ISO base media
file format of ISO/IEC 14496-12). The HEVC pictures inside are decoded by
[go-avkit/h265](https://github.com/go-avkit/h265). No cgo, no libheif.

```go
import (
	"image"
	_ "github.com/go-images/heif" // registers the formats
)

img, format, err := image.Decode(r) // format is "heif"
cfg, format, err := image.DecodeConfig(r)
```

`image.Decode` finds HEIF by its `ftyp` box (brands `heic`, `heix`, `hevc`, `hevx`, `heim`,
`heis`, `hevm`, `hevs`, `mif1`). The package also offers:

| Function | Purpose |
|---|---|
| `Decode(r)` | the primary image as an `image.Image` |
| `DecodeOptions(r, Options)` | the same with explicit `Limits`; returns a `*Result` with `Image`, `ICCProfile`, `Nclx` and `HasAlpha` |
| `DecodeConfig(r)` / `DecodeConfigOptions` | size and colour model from the head of the file, without decoding any picture |
| `DecodeWork(r, opts)` | the pixels decoding the primary image will process (every tile of a grid, however far it is cropped), from the head of the file: what a caller sets a budget against |

The result is an `*image.YCbCr` or `*image.Gray` sharing the decoder's planes when nothing
needs converting (8-bit, full-range BT.601, no transform), otherwise an `*image.RGBA`,
`*image.RGBA64`, `*image.Gray` or `*image.Gray16` (the 16-bit forms for deeper sources).
Rotation (`irot`), mirroring (`imir`) and cropping (`clap`)
are applied, so `DecodeConfig` reports the size after them. Grid items (`grid`), identity
derivations (`iden`) and items stored in `idat` are supported. Colour conversion follows the
`nclx` colour information (matrix, range); an ICC profile is returned, not applied.

`imir`: axis 0 swaps top and bottom, axis 1 swaps left and right, as libheif does. The
`irot` direction (anti-clockwise) was checked against Apple's `sips`; `sips` cannot settle the
mirror axis, so that one follows libheif.

## Security

Input is untrusted. Every size is checked before anything is allocated from it.

```go
res, err := heif.DecodeOptions(r, heif.Options{Limits: heif.Limits{
	MaxBytes:  64 << 20, // bytes read from r (default 256 MiB)
	MaxPixels: 1 << 24,  // pixels of any picture, coded or output (default 1<<26)
}})
```

`MaxPixels` is also the memory bound of a decode. The HEVC planes and the converted image are
alive together, and a crop or rotation is written as the pixels are converted, with no copy
of the unturned picture: 5.5 bytes a pixel live at the peak for an 8-bit 4:2:0 picture
(measured heap, garbage included: 352 to 437 MiB for a flat 8192x8192 file of 17 KB with
`irot`, against 608 MiB when the picture was converted and then turned), 14 for 16-bit 4:4:4 (6 for the planes, 8 for the result; 896 MiB
at the default `1<<26`). Set `MaxPixels` to your budget divided by that figure.

The coded size comes from the SPS of the `hvcC`, not from `ispe`: an item whose parameter set
does not state the size its `ispe` does is refused before any picture is decoded, and
`DecodeWork` reports the coded size (a picture cropped by its conformance window counts as the
whole coded picture, and every tile of a grid counts, up to four times `MaxPixels`).

`Limits` also bounds tiles, boxes, nesting depth, items and the decoder's memory
(`MaxTiles`, `MaxBoxes`, `MaxDepth`, `MaxItems`, `MaxDecoderBytes`). A zero field takes the
default. Refusals are named errors you can test with `errors.Is`: `ErrNotHEIF`, `ErrTruncated`,
`ErrInvalid`, `ErrUnsupported`, `ErrNoPrimaryItem`, `ErrCorrupt`, `ErrFileTooLarge`,
`ErrImageTooLarge`, `ErrTooManyBoxes`, `ErrNestingTooDeep`, `ErrTooManyItems`,
`ErrTooManyTiles`. Overlapping `iloc` extents count against `MaxBytes` once each.

Tests: `FuzzDecode` with a committed corpus (`testdata/fuzz/FuzzDecode`), and `TestBombs`,
which asserts a refusal with a small allocation for enormous declared sizes (CI runs it under
`GOMEMLIMIT=256MiB`).

## Not supported

Reported as `ErrUnsupported`, never silently wrong:

- items that are not HEVC (AVC, AV1, JPEG, uncompressed) and `moov` image sequences;
- alpha: `HasAlpha` reports an auxiliary alpha plane, which is not decoded;
- HDR gain maps and other auxiliary images;
- `iloc` construction method 2 (item offsets) and external data references;
- essential properties the reader does not know.

Sources deeper than 8 bits decode (10 and 12 bit, 4:0:0, 4:2:0, 4:2:2, 4:4:4) to 16-bit
output. Bit depths are those that `go-avkit/h265` v0.24.0 decodes.

## Checked against

- grid decode is byte-identical to ffmpeg 9.0.2 on a 2x2 grid (`TestGridMatchesFFmpeg`);
- a single image from Apple's HEIC encoder matches `sips` at 52 dB PSNR;
- locally, 51 of the 64 Nokia HEIF conformance files decode; the others are image sequences
  without a `meta` box (11), an unknown essential property `rref` (1) and an AVC item (1),
  each refused with a named error (`TestLocalFiles`, run with `HEIF_LOCAL_DIRS`).
  Those files and libheif's `example.heic` have no licence that allows committing them, so
  they are used locally only.

The reference tool, libheif's `heif-dec`, was not installed when this was written, so there
is no timing comparison against it yet. `BenchmarkDecodeGrid` and `BenchmarkDecodeSmall`
(`GOMAXPROCS=1 gotest -run XXX -bench Decode .`) are the harness; they were not run for this
first version because the machine was above the fleet's load limit.

## Development

`go test ./...` runs everything; the package keeps 100% statement coverage
(`go run ./internal/covergate cover.out`). `internal/mkhevc` makes the HEVC test streams with
ffmpeg; `internal/mkpng` writes the reference PNGs.

BSD 3-Clause.
