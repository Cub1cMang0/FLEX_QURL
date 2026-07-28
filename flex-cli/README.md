# flex-convert-cli

A headless CLI wrapper around FLEX's existing `MainImageConverter` class.
No GUI, no Qt Widgets, no window system needed -- runs with the offscreen
platform plugin so it's suitable for a server/container environment.


## Build

Requires Qt5 (Core + Gui) dev headers and CMake.

```bash
# Debian/Ubuntu
apt-get install qtbase5-dev cmake

# Fedora / dnf distros
sudo dnf install qt5-qtbase-devel cmake 

mkdir build && cd build
cmake ..
cmake --build .

```

## Run

```bash
QT_QPA_PLATFORM=offscreen ./flex-convert-cli <input_file> <output_dir> <output_ext>

# example
QT_QPA_PLATFORM=offscreen ./flex-convert-cli photo.png ./out ico
```