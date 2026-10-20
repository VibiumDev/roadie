# How to Install Raspberry Pi OS for Roadie

Step 1 of 2 for a new Roadie. Step 2 is
[Configure Raspberry Pi OS for Roadie](configure-os.md), which runs one
script that does the rest.

## What you need

- Raspberry Pi 4 (2 GB is enough; 4 GB if you'll use its desktop)
- MicroSD card, 32 GB or more. A high-endurance card for a Pi that runs
  around the clock.
- MicroSD card reader
- Official Raspberry Pi USB-C power supply (5.1 V, 3 A)
- Ethernet cable. A phone host shares this connection with the phone, so use
  wired, not Wi-Fi.

## 1. Get Raspberry Pi Imager

Download and install [Raspberry Pi Imager](https://www.raspberrypi.com/software/)
for your computer. These steps are for Imager 2.0 or later, which walks
through **Device**, **OS**, **Storage**, **Customisation** and **Writing**
in order.

## 2. Device

Choose **Raspberry Pi 4**.

## 3. OS

| Pick | When |
|---|---|
| **Raspberry Pi OS Lite (64-bit)**, under *Raspberry Pi OS (other)* | A Pi that hosts a phone, headless. Smaller, less running. |
| **Raspberry Pi OS (64-bit)** | You want a desktop on the Pi, or will run the browser tests (`make test-e2e-pi`) |

Roadie needs the 64-bit OS.

## 4. Storage

Choose the microSD card. Leave **Exclude system drives** on, so your
computer's own disk can't be picked by mistake.

## 5. Customisation

Click **NEXT** after each screen.

- **Hostname**: `roadie`, or numbered when you'll have several:
  `roadie-01`, `roadie-02`… Each Roadie on a network needs its own. It also
  becomes Roadie's name: `http://roadie-01.local:8080`.
- **Localisation**: your time zone and keyboard (optional).
- **User**: any username and a strong password; keep it in a password
  manager. Roadie runs as whoever runs the set-up script in step 2.
- **Wi-Fi**: leave blank and use Ethernet, especially on a phone host.
  Fill it in only for a bench Pi with no cable.
- **Remote access**: turn on **SSH**. For more than one Pi, choose
  **public-key authentication** and paste your public key; password
  authentication is fine for one.
- **Raspberry Pi Connect**: leave off.

## 6. Write

Check the summary and click **WRITE**, then **I UNDERSTAND, ERASE AND WRITE**.
Allow the admin prompt if one appears. Writing and verifying takes a few
minutes; Imager ejects the card when it's done.

## 7. Boot

Put the card in the Pi, plug in Ethernet, then power it from its own
supply.

Don't connect a phone yet. With the linux-gadget backend, the Pi's USB-C
port (its power input) also becomes the phone's connection, and wiring that
safely comes after set-up:
[Wiring and power](linux-gadget.md#wiring-and-power).

Next: [Configure Raspberry Pi OS for Roadie](configure-os.md).
