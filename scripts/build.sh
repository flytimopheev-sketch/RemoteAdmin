#!/bin/bash
# RemoteAdmin 1.8.0 build script
# Builds: linux binary, RPM (РЕД ОС 8), .deb (Astra/Debian), AppImage
set -e
cd "$(dirname "$0")/.."   # repo root
export VERSION=1.9.0
export RELEASE=1
export PATH="$HOME/rpmroot/usr/bin:$PATH"
export PKGNAME=remoteadmin
export BUILDDIR=${BUILDDIR:-$HOME/rpmroot}
export OUT=${OUT:-/home/z/my-project/download}

source "$HOME/env.sh" 2>/dev/null || true
export GOFLAGS=-mod=vendor

echo "== 1. Icon =="
go run scripts/genicon.go icon.png 2>/dev/null || go run scripts/genicon.go /tmp/icon.png
[ -f icon.png ] || cp assets/icon.png icon.png

echo "== 2. Binary =="
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o remoteadmin ./cmd/remoteadmin

echo "== 3. RPM =="
rm -rf "$BUILDDIR"/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
mkdir -p "$BUILDDIR"/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
TARBALL="$PKGNAME-$VERSION.tar.gz"
tar --exclude='remoteadmin/.git' --exclude='remoteadmin/remoteadmin' --exclude='remoteadmin/icon.png' --transform "s|^remoteadmin[^/]*|remoteadmin-${VERSION}|" \
  -czf "$BUILDDIR/SOURCES/$TARBALL" -C .. "$(basename $PWD)"
LD_LIBRARY_PATH="$HOME/rpmroot/usr/lib/x86_64-linux-gnu" RPM_CONFIGDIR="$HOME/rpmroot/usr/lib/rpm" "$HOME/rpmroot/usr/bin/rpmbuild" -ba --define "_topdir $BUILDDIR" \
  --define "_dbpath $BUILDDIR/rpmdb" --define "_rpmconfigdir $HOME/rpmroot/usr/lib/rpm" \
  --define "pkgname $PKGNAME" --define "version $VERSION" --define "release $RELEASE" \
  packaging/rpm/remoteadmin.spec 2>&1 | tail -3
RPM=$(ls "$BUILDDIR"/RPMS/x86_64/${PKGNAME}-${VERSION}-*.x86_64.rpm | head -1)
cp "$RPM" "$OUT/"
echo "RPM: $RPM"

echo "== 4. DEB =="
DEBDIR="$BUILDDIR/debroot"
rm -rf "$DEBDIR"; mkdir -p "$DEBDIR/DEBIAN" \
  "$DEBDIR/usr/bin" \
  "$DEBDIR/usr/share/applications" \
  "$DEBDIR/usr/share/icons/hicolor/128x128/apps" \
  "$DEBDIR/usr/share/doc/$PKGNAME"
install -m755 remoteadmin "$DEBDIR/usr/bin/remoteadmin"
install -m644 icon.png "$DEBDIR/usr/share/icons/hicolor/128x128/apps/remoteadmin.png"
cat > "$DEBDIR/usr/share/applications/remoteadmin.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=RemoteAdmin
GenericName=SSH bulk host manager
Comment=SSH-менеджер группы хостов
Exec=remoteadmin
Icon=remoteadmin
Terminal=false
Categories=Network;System;RemoteAccess;
Keywords=ssh;admin;remote;hosts;
EOF
cat > "$DEBDIR/DEBIAN/control" <<EOF
Package: remoteadmin
Version: $VERSION
Section: net
Priority: optional
Architecture: amd64
Depends: libgtk-3-0, libgdk-pixbuf-2.0-0, openssh-client
Recommends: notify-send | libnotify-bin
Maintainer: RemoteAdmin <admin@remoteadmin.local>
Description: Современный SSH-менеджер группы хостов
 Бескаркасное GTK3-приложение для массового управления хостами по SSH:
 playbook-задачи, шаблоны, цепочки с rollback, мониторинг CPU/RAM/дисков,
 двухпанельный SFTP-менеджер, мульти-терминал (ClusterSSH), импорт/экспорт
 Ansible inventory, отчёты HTML/CSV. Пароли шифруются AES-256-GCM,
 проверяются ключи хостов (known_hosts).
EOF
cat > "$DEBDIR/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if command -v update-desktop-database >/dev/null 2>&1; then update-desktop-database -q || true; fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true; fi
exit 0
EOF
chmod 755 "$DEBDIR/DEBIAN/postinst"
echo "This is RemoteAdmin $VERSION — modern SSH bulk host manager." > "$DEBDIR/usr/share/doc/$PKGNAME/changelog"
echo "См. /usr/share/doc/remoteadmin/README" >> "$DEBDIR/usr/share/doc/$PKGNAME/changelog"
DEB="$OUT/${PKGNAME}_${VERSION}_amd64.deb"
dpkg-deb --build --root-owner-group "$DEBDIR" "$DEB" 2>&1 | tail -1
echo "DEB: $DEB"

echo "== 5. AppImage =="
APPDIR="$BUILDDIR/appimage/RemoteAdmin.AppDir"
rm -rf "$BUILDDIR/appimage"; mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/icons/hicolor/128x128/apps" "$APPDIR/usr/share/applications"
install -m755 remoteadmin "$APPDIR/usr/bin/remoteadmin"
install -m644 icon.png "$APPDIR/usr/share/icons/hicolor/128x128/apps/remoteadmin.png"
install -m644 icon.png "$APPDIR/remoteadmin.png"
cat > "$APPDIR/usr/share/applications/remoteadmin.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=RemoteAdmin
GenericName=SSH bulk host manager
Comment=SSH-менеджер группы хостов
Exec=remoteadmin
Icon=remoteadmin
Terminal=false
Categories=Network;System;RemoteAccess;
EOF
cp "$APPDIR/usr/share/applications/remoteadmin.desktop" "$APPDIR/remoteadmin.desktop"
# AppRun: bundle GTK3 libs + launch
cat > "$APPDIR/AppRun" <<'EOF'
#!/bin/bash
# RemoteAdmin AppRun: portable GTK3 runtime
SELF="$(readlink -f "$0")"
DIR="${SELF%/*}"
export APPDIR="$DIR"
LD="$DIR/usr/lib"
export LD_LIBRARY_PATH="$LD:${LD_LIBRARY_PATH}"
export GTK_PATH="$LD/gtk-3.0"
export GDK_PIXBUF_MODULE_FILE="$DIR/usr/lib/gdk-pixbuf-2.0/2.10.0/loaders.cache"
export GI_TYPELIB_PATH="$LD/girepository-1.0"
export XDG_DATA_DIRS="$DIR/usr/share:${XDG_DATA_DIRS}:/usr/share"
exec "$DIR/usr/bin/remoteadmin" "$@"
EOF
chmod 755 "$APPDIR/AppRun"
# bundle libs
mkdir -p "$APPDIR/usr/lib"
RPATH=$HOME/gtkroot/usr/lib/x86_64-linux-gnu
for lib in libgtk-3.so.0 libgdk-3.so.0 libgdk_pixbuf-2.0.so.0 libgio-2.0.so.0 \
           libgobject-2.0.so.0 libglib-2.0.so.0 libgmodule-2.0.so.0 \
           libpango-1.0.so.0 libpangocairo-1.0.so.0 libpangoft2-1.0.so.0 \
           libcairo.so.2 libcairo-gobject.so.2 libatk-1.0.so.0 libatk-bridge-2.0.so.0 \
           libepoxy.so.0 libharfbuzz.so.0 libharfbuzz-subset.so.0 libharfbuzz-gobject.so.0 \
           libfontconfig.so.1 libfreetype.so.6 libpixman-1.so.0 libpng16.so.16 \
           libxcb.so.1 libxcb-shm.so.0 libxcb-render.so.0 libxkbcommon.so.0 \
           libxkbcommon-x11.so.0 libX11.so.6 libXext.so.6 libXrender.so.1 libXcursor.so.1 \
           libXrandr.so.2 libXinerama.so.1 libXfixes.so.3 libXi.so.6 libXcomposite.so.1 \
           libXdamage.so.1 libwayland-client.so.0 libwayland-cursor.so.0 libwayland-egl.so.1 \
           libfribidi.so.0 libthai.so.0 libdatrie.so.1 libgraphite2.so.3 \
           libjpeg.so.62 libtiff.so.6 libwebp.so.7 libwebpdemux.so.2 libwebpmux.so.3 \
           libzstd.so.1 libbrotlidec.so.1 libbrotlicommon.so.1 liblzma.so.5 libexpat.so.1 \
           libatspi.so.0 libdbus-1.so.3 libcloudproviders.so.0 libcolord.so.2 liblcms2.so.2 \
           libdeflate.so.0 libjbig.so.0 liblerc.so.4 libsharpyuv.so.0 libavif.so.16 \
           libdav1d.so.7 libaom.so.3 libgav1.so.1 libyuv.so.0 librsvg-2.so.2 libcroco-0.6.so.3 \
           libxml2.so.2 libmount.so.1 libblkid.so.1 libselinux.so.1 libpcre2-8.so.0 \
           libffi.so.8 libjson-glib-1.0.so.0 libpsl.so.5 libnghttp2.so.14 libssh2.so.1; do
  [ -f "$RPATH/$lib" ] && cp -fL "$RPATH/$lib" "$APPDIR/usr/lib/" 2>/dev/null || true
done
# gtk engines/gio modules/pixbuf loaders
cp -r "$RPATH/gtk-3.0" "$APPDIR/usr/lib/" 2>/dev/null || true
cp -r "$RPATH/gio" "$APPDIR/usr/lib/" 2>/dev/null || true
mkdir -p "$APPDIR/usr/lib/gdk-pixbuf-2.0/2.10.0"
cp -r "$RPATH/gdk-pixbuf-2.0/2.10.0/loaders" "$APPDIR/usr/lib/gdk-pixbuf-2.0/2.10.0/" 2>/dev/null || true
LD_LIBRARY_PATH="$APPDIR/usr/lib" "$RPATH/gdk-pixbuf-2.0/gdk-pixbuf-query-loaders" "$APPDIR"/usr/lib/gdk-pixbuf-2.0/2.10.0/loaders/*.so 2>/dev/null | sed "s|$APPDIR/usr/lib/gdk-pixbuf-2.0/2.10.0/loaders/||g" > "$APPDIR/usr/lib/gdk-pixbuf-2.0/2.10.0/loaders.cache" || true
# glib schemas + icons minimal
mkdir -p "$APPDIR/usr/share/glib-2.0/schemas"
cp -f "$HOME/gtkroot/usr/share/glib-2.0/schemas/"*.xml "$APPDIR/usr/share/glib-2.0/schemas/" 2>/dev/null || true
( cd "$APPDIR/usr/share/glib-2.0/schemas" && "$HOME/gtkroot/usr/bin/glib-compile-schemas" . 2>/dev/null ) || \
  glib-compile-schemas "$APPDIR/usr/share/glib-2.0/schemas" 2>/dev/null || true
mkdir -p "$APPDIR/usr/share/icons/hicolor/16x16/status" "$APPDIR/usr/share/icons/Adwaita/16x16/status"
[ -d "$HOME/gtkroot/usr/share/icons" ] && cp -rn "$HOME/gtkroot/usr/share/icons/"* "$APPDIR/usr/share/icons/" 2>/dev/null || true
# runtime for AppImage
RUNTIME="$BUILDDIR/appimage/runtime"
if [ ! -f "$RUNTIME" ]; then
  curl -sL --max-time 120 -o "$RUNTIME" https://github.com/AppImage/AppImageKit/releases/download/continuous/runtime-x86_64 || true
fi
SQFS="$BUILDDIR/appimage/remoteadmin.squashfs"
MK=mksquashfs
command -v $MK >/dev/null 2>&1 || MK="$HOME/rpmroot/usr/bin/mksquashfs"
command -v $MK >/dev/null 2>&1 || MK=""
if [ -n "$MK" ] && [ -f "$RUNTIME" ]; then
  "$MK" "$APPDIR" "$SQFS" -comp xz -root-owned -noappend -quiet 2>&1 | tail -1
  cat "$RUNTIME" "$SQFS" > "$OUT/RemoteAdmin-$VERSION-x86_64.AppImage"
  chmod +x "$OUT/RemoteAdmin-$VERSION-x86_64.AppImage"
  echo "AppImage: $OUT/RemoteAdmin-$VERSION-x86_64.AppImage"
else
  echo "AppImage: SKIP (нет mksquashfs или runtime)"
fi

echo "== 6. Source tarball =="
tar --exclude='remoteadmin/.git' --exclude='remoteadmin/remoteadmin' --exclude='remoteadmin/icon.png' --transform "s|^remoteadmin[^/]*|remoteadmin-${VERSION}|" -czf "$OUT/${PKGNAME}-${VERSION}-source.tar.gz" -C .. "$(basename $PWD)"
echo "SRC: $OUT/${PKGNAME}-${VERSION}-source.tar.gz"

echo "== DONE =="
ls -la "$OUT" | grep -E "rpm|deb|AppImage|tar.gz"
