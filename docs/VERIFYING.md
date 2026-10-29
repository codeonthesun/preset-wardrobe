# Checking a download

Every release on this repository is built by GitHub Actions from the code at that release's tag
(see `.github/workflows/release.yml`). Nobody uploads the files by hand. Two ways to check yours:

## 1. Compare the checksum

Each release has `SHA256SUMS.txt`. In PowerShell, in the folder with your download:

```powershell
Get-FileHash .\PresetWardrobe.exe -Algorithm SHA256
```

The hash should match the line for `PresetWardrobe.exe` in `SHA256SUMS.txt`.

## 2. Verify where it was built

With the [GitHub CLI](https://cli.github.com):

```powershell
gh attestation verify .\PresetWardrobe.exe --repo OWNER/REPO
```

This confirms the file was built by this repository's release workflow, and from which commit.

## Why Windows warns about it

The .exe isn't signed with a paid code-signing certificate, so Windows SmartScreen shows
"Windows protected your PC" the first time. Check the file as above, then choose
**More info > Run anyway**.
