# Dependency setup

Run `./setup.sh` as your normal user. It offers to install missing packages
with APT on Ubuntu, Linux Mint and Debian, DNF on Fedora, rpm-ostree on
Bazzite, or pacman on Arch and its derivatives. It asks before it uses sudo.
`--yes` accepts these prompts.

Run `./setup.sh --deps-only` to check dependencies without downloading or
installing GG. Add `--yes` to accept package installation.
Required package failures stop setup with commands to retry. Recommended
packages install separately, so an unavailable package does not stop setup.
Setup does not add repositories or change your audio services.

## Ubuntu, Linux Mint and Debian

Install the build dependencies:

```sh
sudo apt-get update
sudo apt-get install golang-go build-essential libreadline-dev p7zip-full curl
```

`7zip` can replace `p7zip-full` on releases that no longer ship it.
The extractor accepts `7z`, `7zz` and `7za`.
If APT cannot find a package, check your release and enabled repositories.
On Ubuntu, these packages can require the Universe repository:

```sh
sudo add-apt-repository universe
sudo apt-get update
```

Install the recommended tools:

```sh
sudo apt-get install pipewire-bin wireplumber pulseaudio-utils ffmpeg wl-clipboard xdg-utils
```

Sonar needs a working PipeWire session. Installing its tools alone does not
switch an older Mint or Ubuntu installation from PulseAudio to PipeWire.
Use your distribution's instructions to set up PipeWire if needed.

## Fedora

Install the build dependencies:

```sh
sudo dnf install golang gcc readline-devel 7zip curl
```

On older Fedora releases without `7zip`, install `p7zip p7zip-plugins` instead.
Install the recommended tools:

```sh
sudo dnf install pipewire-utils wireplumber pulseaudio-utils ffmpeg-free wl-clipboard xdg-utils
```

Fedora's [`ffmpeg-free`](https://packages.fedoraproject.org/pkgs/ffmpeg/ffmpeg-free/) has a limited set of codecs. Moments exports can
need a full FFmpeg build with H.264/AAC support from your chosen codec repository.
Setup does not add that repository or replace codec packages.

## Bazzite

Run setup on the Bazzite host from a desktop terminal, outside Distrobox:

```sh
./setup.sh
```

Setup detects Bazzite before Fedora, even when DNF is present. It offers to
stage missing system packages with `rpm-ostree`. If Electron and npm are
missing, setup includes npm in the same required package transaction.
After a successful transaction, setup stops before the GG download or build.
Reboot when ready, then run setup again with the same options.
`--yes` accepts package installation but does not reboot the system.

To install the build dependencies by hand:

```sh
sudo rpm-ostree install --idempotent --allow-inactive golang gcc readline-devel 7zip curl npm
```

Reboot after the command succeeds. Then install Electron for your user:

```sh
npm install --prefix "${PREFIX:-$HOME/.local}/lib/steelseries-gg/electron" electron@39
./setup.sh
```

Most Bazzite images already include audio and desktop tools. Setup offers
missing recommended tools separately. Successful installs also need a reboot
before setup can continue. Unavailable optional packages do not block setup.
Do not use the regular Fedora DNF commands on the Bazzite host.

Bazzite recommends other installation methods before package layering.
This source installer needs host build dependencies and device access.
See [Bazzite's package layering guide](https://docs.bazzite.gg/Installing_and_Managing_Software/rpm-ostree/)
for how layered packages affect system updates.
The installer tests simulate Bazzite. Full application operation on Bazzite
needs testing on a Bazzite system.

## Arch and derivatives

```sh
sudo pacman -S --needed go gcc readline 7zip curl electron39
sudo pacman -S --needed pipewire wireplumber libpulse ffmpeg wl-clipboard xdg-utils
```

If `electron39` is unavailable, use the local Electron installation below.

## Electron on any distribution

Electron 37 and 39 are tested. Ubuntu, Mint and Fedora do not necessarily
ship a suitable Electron package. Install Electron 39 for your user with npm.
First install npm:

```sh
# Ubuntu, Mint, Debian
sudo apt-get install npm
# Fedora
sudo dnf install npm
# Bazzite: reboot after this command, before running npm
sudo rpm-ostree install --idempotent --allow-inactive npm
# Arch
sudo pacman -S --needed npm
```

Then install Electron without sudo:

```sh
npm install --prefix "${PREFIX:-$HOME/.local}/lib/steelseries-gg/electron" electron@39
./setup.sh
```

Setup and the installed launcher find this runtime automatically, including
when you open GG from the application menu. This download needs network access
to npm and Electron's release servers. npm must permit Electron's install script.
For another Electron installation, set `GG_LINUX_ELECTRON` to its executable path.

If Electron reports missing shared libraries, install its desktop libraries:

```sh
# Ubuntu 24.04+, Mint 22+, Debian 13+
sudo apt-get install libgtk-3-0t64 libnss3 libasound2t64 libgbm1 libxss1 libatk-bridge2.0-0t64
# Ubuntu 22.04, Mint 21, Debian 12
sudo apt-get install libgtk-3-0 libnss3 libasound2 libgbm1 libxss1 libatk-bridge2.0-0
# Fedora
sudo dnf install gtk3 nss alsa-lib mesa-libgbm libXScrnSaver at-spi2-atk
# Bazzite: reboot after this command
sudo rpm-ostree install --idempotent --allow-inactive gtk3 nss alsa-lib mesa-libgbm libXScrnSaver at-spi2-atk
```

Electron also needs a graphical desktop session. Do not disable its sandbox
to fix a library error. See the [Electron installation guide](https://www.electronjs.org/docs/latest/tutorial/installation)
for download errors.

## Go version and compiler errors

Go 1.21 or newer can download the toolchain specified in `go.mod`. The
build needs network access for that toolchain and the Go modules.
If setup reports an older Go version, install a current Go release with the
[official Go installation steps](https://go.dev/doc/install), then add
`/usr/local/go/bin` to your PATH before the distribution's Go:

```sh
export PATH="/usr/local/go/bin:$PATH"
./setup.sh
```

If Go cannot select the required toolchain, check your network connection.
If you deliberately disabled automatic toolchain downloads, install the version
in `go.mod` yourself, or allow downloads for this setup run:

```sh
GOTOOLCHAIN=auto ./setup.sh
```

The build also needs C support. If you disabled CGO, run:

```sh
CGO_ENABLED=1 ./setup.sh
```

A readline failure needs the development package, including headers and the
linkable library: `libreadline-dev` on APT, `readline-devel` on Fedora, or
`readline` on Arch. Setup compiles a small program to check both.

## Optional features

- GPU capture needs [gpu-screen-recorder](https://git.dec05eba.com/gpu-screen-recorder/about/).
  Install it with the upstream instructions or your distribution's package manager.
  The package is not in every distribution's default repositories.
- Wayland capture can use `wf-recorder` on supported wlroots compositors.
  X11 capture can use FFmpeg. These alternatives have different desktop support.
- Sonar's extra effects need DeepFilterNet's LADSPA plugin, LSP plugins,
  PipeWire's SOFA plugin and libmysofa. See [Sonar](sonar.md).

After you install missing dependencies, run `./setup.sh` again.
