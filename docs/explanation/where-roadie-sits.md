# Where Roadie Sits

There are three places to drive a phone from: inside it, its cables, and in
front of it. Roadie works from the cables.

| | Inside | Cables: Roadie | In front |
|---|---|---|---|
| What | An agent app on the phone: WebDriverAgent on iOS, UiAutomator on Android (what Appium drives) | A USB keyboard, mouse, touchscreen and microphone; HDMI video and USB audio back | A robot finger on the glass, a camera over the screen, actuators on the side buttons |
| On the phone | A test app, signed and kept running | Nothing | Nothing |
| Sees | The element tree, and screenshots the OS hands over | The pixels the phone sends out | The glass, through a lens |
| Acts | Through the OS's testing hooks | As those USB devices | As a hand |

## Inside

WebDriverAgent runs on the iPhone as an XCTest runner, with the hooks Apple
gives its own test tools: it reads every element, taps one by name, sets the
orientation, presses Home and the volume buttons, and locks the phone.
UiAutomator, over adb, does the same on Android.

On iOS the price is a Mac with Xcode, a developer account, and a build signed
for each phone, which is signed again when its profile runs out and broken
often by iOS and Xcode updates; on Android it is USB debugging left on. Either
way the phone is in a mode a customer's isn't, and some apps notice.

## Cables

Nothing is installed and nothing is unlocked for developers: the phone stays
as its owner's would be, and the same box drives an iPhone, an Android phone
or a laptop. Roadie sees what a person sees and does what a person does with
a keyboard, a mouse and their voice.

What a person does with a hand instead goes through the phone's
accessibility features, the ones built for people who can't:

| On an iPhone | Through |
|---|---|
| Tap, swipe, long press | AssistiveTouch's pointer, with Perform Touch Gestures |
| Home | ⌘H |
| Lock, and wake | Ctrl+⌘Q, and any key |
| Turn the screen | AssistiveTouch's menu, opened by a mouse button: [`/api/rotate`](../../API.md#post-apirotate) |
| Two fingers at once | Out of reach: AssistiveTouch has one pointer |
| Volume | Out of reach so far: iOS ignores a keyboard's volume keys |

On Android the touchscreen takes two fingers at once.

These are out of reach from the cables altogether:

- turning a phone on
- Face ID and Touch ID
- the phone's own microphone and cameras ("Hey Siri" didn't answer Roadie's
  microphone)
- the sensors, such as turning the phone in a hand

## In front

A robot reaches what the cables can't: the side buttons, two fingers, the
sensors and a phone that is off. It costs the most per phone, in hardware and
room, and its camera sees glare where HDMI sees pixels.

## Together

Each reaches what the others can't, and each checks the others: the agent
says it tapped, HDMI shows whether the screen changed, and the camera shows
whether the glass did. HDMI isn't the glass: protected video can go black on
HDMI while it plays on the phone.
