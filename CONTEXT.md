# LogScene

A Windows application that captures images from webcams on a schedule and compiles them into timelapse videos.

## Capture

**Webcam**:
A named capture source — a physical or network camera plus its LogScene configuration (URL, schedule, timezone).
_Avoid_: camera, feed, source

**Image**:
A single JPEG captured from a webcam at a scheduled time.
_Avoid_: frame, photo, screenshot

**Capture**:
The act of fetching an image from a webcam and writing it to storage. "Capture" is the action; "image" is the result.
_Avoid_: fetch, grab

**Render**:
Compiling a webcam's captured images into a timelapse video.
_Avoid_: compile, export, encode, generate

## UI

**Status indicator**:
The colored badge on each webcam card showing its current state: green "Active", yellow "Issues", red "Error", black "Disabled".
_Avoid_: badge, icon, light, dot

**Idle window**:
The period after a webcam's DayLast and before its DayFirst the following day, when no captures are scheduled. Used as the safe window for goroutine restarts and maintenance operations.

## Licensing

**Trial**:
The default unlicensed mode — no license key provided. Limited to one webcam. Enforced progressively: captures stop after day 30, renders stop after day 37.
_Avoid_: demo, evaluation, free version

**Licensed**:
The mode after a valid license key is provided. Displayed to the user as "Licensed — up to N webcams."
_Avoid_: activated, registered, unlocked, purchased

**License key**:
A signed bearer token that activates the product. Encodes a webcam cap and a major version. Stored in the Windows registry; also accepted via the `LOGSCENE_LICENSE` environment variable.
_Avoid_: serial number, activation code, product key, registration key

**Individual**:
A license tier for personal use; up to 10 webcams. Technically identical to Commercial-10 — the distinction is terms of service only.

**Commercial**:
A license tier for business use; up to 10 webcams (Commercial-10) or unlimited (Commercial-Unlimited).

**Upgrade Assurance**:
An annual subscription that entitles the holder to receive new major-version license keys at no cost when they ship.
_Avoid_: maintenance plan, support contract
