# Running bubbles as a Windows service

`bubbles service install` (from an elevated prompt) does all of this for you.
The manual equivalent, for administrators who prefer to see every step:

1. Copy `bubbles.exe` to `C:\Program Files\bubbles\bubbles.exe`.
2. Create the service (note the space after each `=`). The binary detects that
   the service manager started it and defaults its data directory to
   `%ProgramData%\bubbles`:

   ```
   sc.exe create bubbles binPath= "\"C:\Program Files\bubbles\bubbles.exe\" serve --data-dir \"C:\ProgramData\bubbles\"" start= auto DisplayName= "bubbles"
   sc.exe failure bubbles reset= 86400 actions= restart/5000/restart/30000/restart/60000
   ```

3. Allow inbound connections from private networks only:

   ```
   netsh advfirewall firewall add rule name="bubbles HTTPS" dir=in action=allow protocol=TCP localport=28225 profile=private
   netsh advfirewall firewall add rule name="bubbles onboarding" dir=in action=allow protocol=TCP localport=28226 profile=private
   ```

4. Start it: `sc.exe start bubbles`.

Remove it again with:

```
sc.exe stop bubbles
sc.exe delete bubbles
netsh advfirewall firewall delete rule name="bubbles HTTPS"
netsh advfirewall firewall delete rule name="bubbles onboarding"
```

The data directory `C:\ProgramData\bubbles` is created with a protected ACL
(SYSTEM and Administrators only). Back it up with `bubbles backup` rather than
copying files while the service runs.
