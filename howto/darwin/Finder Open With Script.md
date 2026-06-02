Q: can you as script to “open with” options?

Yes! The cleanest way is to create an **Application Bundle** that registers itself as a handler for specific file types.

**Steps:**

1. Open **Automator** → New → **Application**
2. Add **Run Shell Script**, pass input as **arguments**:
   ```bash
   #!/bin/bash
   /path/to/your/script.sh "$@"
   ```
3. Save the app (e.g. `~/Applications/MyTool.app`)

4. Edit the app's `Info.plist` to declare file type associations:
   ```bash
   open ~/Applications/MyTool.app/Contents/Info.plist
   ```
   Add a `CFBundleDocumentTypes` entry:
   ```xml
   <key>CFBundleDocumentTypes</key>
   <array>
       <dict>
           <key>CFBundleTypeExtensions</key>
           <array>
               <string>txt</string>
               <string>log</string>
               <!-- add your extensions -->
           </array>
           <key>CFBundleTypeName</key>
           <string>Text File</string>
           <key>CFBundleTypeRole</key>
           <string>Viewer</string>
       </dict>
   </array>
   ```

5. Re-register with Launch Services so Finder picks it up:
   ```bash
   /System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister \
     -f ~/Applications/MyTool.app
   ```

6. Right-click any matching file → **Open With** — your app should appear.

**Tips:**
- Use `<string>*</string>` as the extension to handle all files
- Change `CFBundleTypeRole` to `Editor` if your tool modifies files
- If it doesn't appear immediately, log out/in or run `lsregister` with `-kill -r -domain local`
