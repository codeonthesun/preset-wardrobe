-- Preset Wardrobe plugin: pictures of your skater presets for the Preset Wardrobe app.
--
-- The snap key (F8 unless you change it in ReSkate's Settings > Controls) on the game's Skaters page
-- snaps the selected preset as one fit for your Fit Catalogue. On a controller, hold Y there.
-- The plugin's page can also walk through all 10 slots in a row.
--
-- Pictures go to <game folder>\Mods\.reskate_plugin_data\PresetWardrobe\snaps\, where
-- Preset Wardrobe picks them up by itself.

local VERSION = "0.2.10"

-- Switched off for now, kept to finish later:
--   PICTURE_REQUESTS: "Update picture" requests from the app, shown as MESSAGES on the page
--   UNSAVED_CHECK:    stopping a snap of a board the game hasn't saved (only the board can be seen)
--   DEV_TOOLS:        the testing cards on the page (timing, turn, stick test, board calibration,
--                     framing) and the F9/F10 stick test
local PICTURE_REQUESTS = false
local UNSAVED_CHECK = false
local DEV_TOOLS = false
-- ALL 10 IN A ROW: switched off while it's being fixed; the page shows it as work in progress.
local WALKTHROUGH = false
local DEFAULT_AREA = {0.27, 0.14, 0.735, 0.985} -- the Skaters page's preview skater (as Fit Check frames it)

-- ---- settings ---------------------------------------------------------------------------------
local defaults = {hold_s = 1, move = 2, count = 10, area = table.clone(DEFAULT_AREA), framing_preview = false,
    spin_deg = 360, turn_sign = 1, board_pictures = true, board_turn2 = -10}

-- The turn. The preview is turned with the menu's own stick (room.turn) and its angle is read back
-- from room.pose. Every snap starts facing front, turns settings.spin_deg (a full 360 by default)
-- while recording, and ends facing front again. 0 degrees takes one picture facing front.
local FRAMES_PER_TURN = 24 -- one every 15 degrees over a full turn
local COVER_DEG = 40       -- the main picture: turned enough to show the board
local FRAME_SIZE, COVER_SIZE = 768, 1024 -- (384 and 512 before; the capture below is at the screen's own size now)
local SPIN_SPEED = 0.9
local MAX_SINGLES = 20     -- older one-fit snaps' files are removed, to stay under the plugin's file limit
local TOLERANCE = math.rad(2.5)
local SETTLE_MS = 180
local settings = reskate.storage.get("snap_settings") or {}
for k, v in defaults do
    if settings[k] == nil then settings[k] = if type(v) == "table" then table.clone(v) else v end
end

local function save_settings()
    reskate.storage.set("snap_settings", settings)
end

-- ---- the snapping job -------------------------------------------------------------------------
-- phases: wait_menu -> hold -> warm -> record -> saving -> move -> hold ... -> done
local job = nil
local last_message = "Not started."
local run_shots = {}
local singles = reskate.storage.get("singles") or {}   -- one-fit snaps, newest last
local fit_seq = reskate.storage.get("fit_seq") or 0

local function now() return reskate.time.now() end

local function wall_clock()
    if os and os.time then return os.time() end
    return 0
end

local function say(text, level)
    last_message = string.sub(text, 1, 200)
    reskate.ui.toast(string.sub(text, 1, 200), level or "info")
end

local function on_skaters_page()
    local menu = reskate.menu.game()
    return menu.open and menu.skaters
end

-- ---- the snap key ------------------------------------------------------------------------------
-- The keyboard key is ReSkate's own bind (Settings > Controls), F8 unless the player changes it. The plugin
-- can't ask ReSkate which key that is, so the app reads it from the player's profile and writes hotkeys.json:
-- {key = "F7", conflicts = {{name = "Fit Check", key = "F7"}}}. Without the app the texts say F8.
-- On a controller, holding Y on the Skaters page snaps too (Y does nothing else there).
local SNAP_ACTION = "Preset Wardrobe: snap a fit"
local PAD_BUTTON, PAD_NAME, PAD_HOLD_MS = "y", "Y", 800
local hotkeys = {key = "F8", conflicts = {}}
local hotkeys_next = 0
local function read_hotkeys()
    local t = now()
    if t < hotkeys_next then return hotkeys end
    hotkeys_next = t + 2000
    local ok, text = pcall(reskate.files.read, "hotkeys.json")
    if not ok or type(text) ~= "string" or text == "" then return hotkeys end
    local okd, data = pcall(reskate.json.decode, text)
    if okd and type(data) == "table" then
        hotkeys.key = if type(data.key) == "string" then data.key else "F8"
        hotkeys.conflicts = if type(data.conflicts) == "table" then data.conflicts else {}
    end
    return hotkeys
end
local function pad_connected()
    if type(reskate.input.gamepad) ~= "function" then return false end
    local ok, pad = pcall(reskate.input.gamepad)
    return ok and type(pad) == "table"
end
-- "F8", or "your snap key" when the player has cleared it in ReSkate's controls.
local function snap_key()
    local key = read_hotkeys().key
    return if key == "" then "your snap key" else key
end
-- "press F8", or "press F8 / hold Y" with a controller connected.
local function snap_hint()
    if pad_connected() then return "press " .. snap_key() .. " / hold " .. PAD_NAME end
    return "press " .. snap_key()
end
-- "F8 cancels.", or "F8 or hold Y cancels." with a controller connected.
local function cancel_hint()
    if pad_connected() then return snap_key() .. " or hold " .. PAD_NAME .. " cancels." end
    return snap_key() .. " cancels."
end

-- The name a snap's pictures share: snaps/<base>.png for one picture, snaps/<base>-1.png ... -8.png
-- for the frames of a turn.
local function base_for(job)
    if job.mode == "test" or job.mode == "boardtest" then return "test" end
    if job.mode == "fit" then return string.format("fit-%05d", job.seq) end
    return string.format("%02d", job.n)
end

-- The kept frame nearest to where the preview had turned `angle` radians, from the turn's track
-- of {frame, travel} pairs (frames numbered as the capture counts them).
local function frame_at(track, kept, angle)
    local best, best_d = nil, math.huge
    for _, t in track do
        local d = math.abs(t.travel - angle)
        if d < best_d then best, best_d = t.frame, d end
    end
    if best == nil then return kept[#kept] end
    local pick, pick_d = kept[1], math.huge
    for _, k in kept do
        local d = math.abs(k - best)
        if d < pick_d then pick, pick_d = k, d end
    end
    return pick
end

-- The preview's heading in radians, 0 facing front, from the menu's own pose of the skater
-- (the same reading Fit Check measures its turns with). nil when the game doesn't give one.
local TAU = 2 * math.pi
local function wrap(a) return (a + math.pi) % TAU - math.pi end
local last_sequence, last_heading = nil, nil
local function wrap_angle(a) return (a + math.pi) % (2 * math.pi) - math.pi end
local function heading()
    if type(reskate.room.pose) ~= "function" then return nil end
    local ok, pose = pcall(reskate.room.pose, "skater")
    if not ok or type(pose) ~= "table" or type(pose.rotation) ~= "table" or #pose.rotation ~= 4 then return nil end
    local q = pose.rotation
    local a, b, c, w = q[1], q[2], q[3], q[4]
    -- (0, 0, 1) turned by q; its angle about the vertical is the heading.
    local tx, ty, tz = 2 * (b * 1 - c * 0), 2 * (c * 0 - a * 1), 2 * (a * 0 - b * 0)
    local x, z = 0 + w * tx + b * tz - c * ty, 1 + w * tz + a * ty - b * tx
    if x * x + z * z < 0.5 then return nil end
    last_sequence, last_heading = pose.sequence, wrap(math.atan2(x, z) + math.pi / 2)
    return last_heading
end

-- Steers the preview toward a heading, a little each tick: true once it's there and still.
-- Which way the stick turns it is learned (and kept) if the first guess makes things worse.
local steer = {target = nil, prev = nil, worse = 0, still_since = nil}
local function steer_to(target, tolerance)
    tolerance = tolerance or TOLERANCE
    local h = heading()
    if h == nil then return nil end
    local err = wrap(target - h)
    if steer.target ~= target then steer.target, steer.prev, steer.worse, steer.still_since = target, nil, 0, nil end
    if math.abs(err) <= tolerance then
        reskate.room.turn(0)
        steer.still_since = steer.still_since or now()
        steer.prev = err
        return now() - steer.still_since >= SETTLE_MS
    end
    steer.still_since = nil
    if steer.prev ~= nil then
        if math.abs(err) > math.abs(steer.prev) + 0.003 then
            steer.worse += 1
        elseif math.abs(err) < math.abs(steer.prev) - 0.003 then
            steer.worse = 0
        end
        if steer.worse >= 5 then
            settings.turn_sign = -(settings.turn_sign or 1)
            save_settings()
            steer.worse = 0
        end
    end
    steer.prev = err
    local speed = math.clamp(math.abs(err) / math.rad(35), 0.15, 0.7)
    reskate.room.turn((if err > 0 then speed else -speed) * (settings.turn_sign or 1))
    return false
end

-- After a snap the preview is brought back to the front, while the next thing goes on.
local homing_until = nil
local function turn_back()
    homing_until = now() + 4000
end

local function write_index()
    local ok, why = reskate.files.write("snaps/index.json", reskate.json.encode({
        version = 2,
        plugin = VERSION,
        run = job and job.run or 0,
        finished = job == nil or job.phase == "done",
        count = settings.count,
        shots = run_shots,
        singles = singles,
    }))
    if not ok then reskate.log("PresetWardrobe: could not write snaps/index.json: " .. tostring(why), "warn") end
end

-- The skater's slots as the game has them now: slot name -> item, and the parameters of the ones that have any
-- (tattoos). n is how many slots there are; the list is empty for a moment now and then.
local function skater_slots()
    local out, params, n = {}, {}, 0
    if type(reskate.outfit) ~= "table" or type(reskate.outfit.slots) ~= "function" then return out, params, n end
    local ok, slots = pcall(reskate.outfit.slots)
    if not ok or type(slots) ~= "table" then return out, params, n end
    for _, sl in ipairs(slots) do
        local okf, name, item, par = pcall(function() return sl.slot, sl.item, sl.parameters end)
        if okf and type(name) == "string" and name ~= "" then
            n = n + 1
            out[name] = type(item) == "string" and item or ""
            if type(par) == "table" and out[name] ~= "" then
                local list = {}
                for _, v in ipairs(par) do if type(v) == "number" then table.insert(list, v) end end
                if #list > 0 then params[name] = list end
            end
        end
    end
    return out, params, n
end

local function worn_keys()
    local keys = {}
    for _, it in reskate.outfit.worn() do table.insert(keys, it.item) end
    for _, it in reskate.outfit.board() do table.insert(keys, it.item) end
    return keys
end

-- Each step's time goes to ReSkate's log ("PresetWardrobe timing: ..."), to see where a snap's
-- time goes.
local function enter(phase)
    if job.phase and job.since then
        reskate.log(string.format("PresetWardrobe timing: %s %d ms", job.phase, now() - job.since))
    end
    job.phase = phase
    job.since = now()
end

local end_board -- below
local function stop_job(message, level)
    if job and job.started then
        if job.phase and job.since then
            reskate.log(string.format("PresetWardrobe timing: %s %d ms", job.phase, now() - job.since))
        end
        reskate.log(string.format("PresetWardrobe timing: the whole snap %d ms (%s)", now() - job.started, tostring(job.mode)))
    end
    if job and (job.phase == "warm" or job.phase == "record" or string.sub(job.phase, 1, 6) == "board_") then reskate.capture.discard() end
    if job and job.board and end_board then end_board(job) end
    job = nil
    if message then say(message, level) end
end

-- Unsaved board changes: the app lists each slot's items as the game last saved them (saved_outfits.json).
-- What's worn not being one of them means the preset has changes the game hasn't saved: the board
-- pictures' trip to the stage would throw them away, and the app couldn't file the snap. So a snap
-- stops first; F8 again within 10 s snaps anyway, without the board pictures (which keeps them).
local unsaved_until = 0
local function outfit_unsaved()
    local text = reskate.files.read("saved_outfits.json")
    if not text then return false end -- no app: nothing to check against
    local ok, data = pcall(reskate.json.decode, text)
    if not ok or type(data) ~= "table" or type(data.slots) ~= "table" then return false end
    -- Items no saved preset has at all (the game's defaults, like its stock wheel colour, aren't
    -- saved in a preset) say nothing either way, so only the rest are compared. On the Skaters page
    -- what's worn is the board's parts, so it's changes to the board this can see.
    local sets, known = {}, {}
    for _, slot in data.slots do
        if type(slot) == "table" and type(slot.items) == "table" then
            local have = {}
            for _, k in slot.items do
                local key = string.lower(tostring(k))
                have[key], known[key] = true, true
            end
            table.insert(sets, have)
        end
    end
    local worn = {}
    for _, w in worn_keys() do
        if known[string.lower(w)] then table.insert(worn, string.lower(w)) end
    end
    local sl = skater_slots()
    for _, item in sl do -- the skater's own slots: clothes, face and body
        if item ~= "" and known[string.lower(item)] then table.insert(worn, string.lower(item)) end
    end
    if #worn == 0 or #sets == 0 then return false end
    for _, have in sets do
        local all = true
        for _, w in worn do
            if not have[w] then all = false break end
        end
        if all then return false end
    end
    return true
end

-- mode: "fit" (the selected preset, for the Fit Catalogue), "all" (slots 1 to 10 in a row) or "test".
local function start_job(mode)
    if job then return stop_job("Snapping cancelled.") end
    local no_board = false
    if UNSAVED_CHECK and mode == "fit" and on_skaters_page() then
        local ok, unsaved = pcall(outfit_unsaved)
        if ok and unsaved then
            if now() > unsaved_until then
                unsaved_until = now() + 10000
                return say("This board isn't any saved preset's: changes the game hasn't saved yet? Save the preset first, then snap. Or " .. snap_hint() .. " again within 10 s to snap anyway, without the board pictures.", "warn")
            end
            no_board = true -- they asked again: the stage would undo the changes
        end
    end
    unsaved_until = 0
    job = {n = 1, mode = mode, test = mode == "test", run = wall_clock(), phase = "wait_menu", since = now(), no_board = no_board,
        started = now()}
    if mode == "all" then run_shots = {} end
    if mode == "boardtest" then
        if not on_skaters_page() then
            job = nil
            return say("Open the Skaters page first, then press Test board pictures.", "warn")
        end
        job.board, job.main, job.frames, job.depth = {}, nil, nil, true
        return enter("board_front")
    end
    if on_skaters_page() then
        enter("hold")
    elseif mode == "all" then
        say("Open the Skaters page and highlight your first (leftmost) preset.")
    else
        say("Open the Skaters page and select the preset to snap.")
    end
end

local function capture_options(skip)
    local a = settings.area
    return {area = {a[1], a[2], a[3], a[4]}, frame = "menu", depth = true, background = true,
        height = 0, skip = skip, frames = 600, seconds = 40} -- the screen's own size: the detail in the pictures starts here
end


-- ---- board pictures ----------------------------------------------------------------------------
-- After a one-fit snap's turn, 4 pictures of the board: bottom, side, griptape, other side. Your
-- skater is shown on the Skaters page's stage (room.open) holding the board up (the game's Board
-- Balance gesture, frozen on its first frame), and the pose is turned rather than the stage: the
-- joints hanging off the skater's placement and the clip's board, all by one rotation about the
-- board's centre. That rotation stands the board straight, squares it to the stage, turns it a
-- further BOARD_TRIM (the stage's camera looks a little off its axes: found by eye, 2026-10-29)
-- and then 90 degrees per view. Up is y.
local BOARD_CLIP = "animation/dingo/c_proto_ofb_gesture_boardbalance_exit_lfoot_01"
-- The least a view is given. Asking for the next one sooner than about 0.5 s after the last is
-- sometimes ignored by the game (tests 38 and 41 lost the griptape view that way).
local BOARD_SETTLE_MS = 500
local BOARD_RETRY_MS = 700 -- no turn seen by then: ask for the view again
local BOARD_WAIT_MS = 1500 -- the most: a slow PC (or drive) gets this long for each view to show
-- How the plugin tells a view has shown: the newest frame differs from the last view's picture
-- (capture.compare, 0..255) and has stopped changing.
-- Measured (test 40): a still frame differs from the last by about 0.11, the turn by 0.43-1.46.
local VIEW_CHANGED, VIEW_STILL = 0.25, 0.2
local BOARD_SIZE = 1024 -- the board fills little of the frame, so it's captured at the screen's own size
local ROOT_CHILDREN = {2, 3, 5, 7, 375, 376}

local function board_supported()
    return type(reskate.room.open) == "function" and type(reskate.animation) == "table"
        and type(reskate.animation.edit) == "function"
end

local function qmul(a, b)
    return {a[4] * b[1] + a[1] * b[4] + a[2] * b[3] - a[3] * b[2],
            a[4] * b[2] - a[1] * b[3] + a[2] * b[4] + a[3] * b[1],
            a[4] * b[3] + a[1] * b[2] - a[2] * b[1] + a[3] * b[4],
            a[4] * b[4] - a[1] * b[1] - a[2] * b[2] - a[3] * b[3]}
end
local function qconj(q) return {-q[1], -q[2], -q[3], q[4]} end
local function qrot(q, v)
    local a, b, c, w = q[1], q[2], q[3], q[4]
    local tx, ty, tz = 2 * (b * v[3] - c * v[2]), 2 * (c * v[1] - a * v[3]), 2 * (a * v[2] - b * v[1])
    return {v[1] + w * tx + (b * tz - c * ty), v[2] + w * ty + (c * tx - a * tz), v[3] + w * tz + (a * ty - b * tx)}
end
local function axis_angle(axis, theta)
    local n = math.sqrt(axis[1] ^ 2 + axis[2] ^ 2 + axis[3] ^ 2)
    if n < 1e-6 or math.abs(theta) < 1e-7 then return {0, 0, 0, 1} end
    local s = math.sin(theta / 2) / n
    return {axis[1] * s, axis[2] * s, axis[3] * s, math.cos(theta / 2)}
end
local function yaw(theta) return axis_angle({0, 1, 0}, theta) end
local function vec3(p) return {p.x or p[1], p.y or p[2], p.z or p[3]} end

local board_clip_name = nil -- the game's name for the Board Balance clip, once found

-- The 4 turned copies of the frozen frame, as clip names; nil and why when it can't.
local function make_board_views()
    if not board_clip_name then
        local names, why = reskate.animation.game_clips("boardbalance")
        if not names then return nil, "wait", why end
        for _, n in names do if n == BOARD_CLIP then board_clip_name = n end end
        if not board_clip_name then
            for _, n in names do
                if string.find(n, "boardbalance", 1, true) and string.find(n, "exit_lfoot", 1, true) then board_clip_name = n end
            end
        end
        if not board_clip_name then return nil, "the game has no Board Balance animation" end
    end
    local clip = "game:" .. board_clip_name
    local info = reskate.animation.info(clip)
    local dt = if (info.frames or 0) > 1 then info.duration / (info.frames - 1) else 0.033
    local short = reskate.animation.trim(clip, 0, math.min(info.duration, dt * 2.5))
    local parts = {}
    for _, j in ROOT_CHILDREN do
        local smp = reskate.animation.sample(short, j, 0)
        if smp then table.insert(parts, {joint = j, q = smp.rotation, p = vec3(smp.position)}) end
    end
    local b = reskate.animation.sample(short, "board", 0)
    if not b then return nil, "the Board Balance animation has no board" end
    local qb, c = b.rotation, vec3(b.position)
    table.insert(parts, {joint = "board", q = qb, p = c})
    -- straighten: the board's most upright axis stood up, then squared to the stage
    local long, best, L = 1, -1, {0, 1, 0}
    for i = 1, 3 do
        local e = {0, 0, 0}
        e[i] = 1
        local r = qrot(qb, e)
        if math.abs(r[2]) > best then best, long, L = math.abs(r[2]), i, r end
    end
    if L[2] < 0 then L = {-L[1], -L[2], -L[3]} end
    local up_fix = axis_angle({-L[3], 0, L[1]}, math.acos(math.clamp(L[2], -1, 1)))
    local other = {0, 0, 0}
    other[if long == 1 then 2 else 1] = 1
    local o = qrot(qmul(up_fix, qb), other)
    local phi = math.atan2(o[1], o[3])
    local square = math.floor(phi / (math.pi / 2) + 0.5) * (math.pi / 2) - phi
    -- the stage's tilts (from calibration) on top: about its two level axes
    local tilt = qmul(axis_angle({1, 0, 0}, math.rad(settings.board_tilt_a2 or 0)), axis_angle({0, 0, 1}, math.rad(settings.board_tilt_b2 or 0)))
    local straight = qmul(tilt, qmul(yaw(square + math.rad(settings.board_turn2 or -10)), up_fix))
    local views = {}
    for k = 0, 3 do
        local t = qmul(yaw(k * math.pi / 2), straight)
        local tracks = {}
        for _, part in parts do
            local d = qrot(t, {part.p[1] - c[1], part.p[2] - c[2], part.p[3] - c[3]})
            table.insert(tracks, {joint = part.joint, keys = {{time = 0, ease = "step",
                rotation = qmul(qconj(part.q), qmul(t, part.q)),
                offset = vector.create(c[1] + d[1] - part.p[1], c[2] + d[2] - part.p[2], c[3] + d[3] - part.p[3])}}})
        end
        views[k + 1] = reskate.animation.edit(short, tracks, {name = "presetwardrobe_board_" .. k})
    end
    return views
end

-- Full screen resolution (height 0), and only as many frames as the 4 views need.
local function board_capture(skip)
    local a = settings.area
    return {area = {a[1], a[2], a[3], a[4]}, frame = "menu", depth = true, background = true,
        height = 0, skip = skip, frames = 400, seconds = 20}
end

local function show_board_view(job)
    local good, why = reskate.animation.play(job.board.views[job.board.k], {paused = true, at = 0, blend = 0, loop = true})
    return good, why
end

end_board = function(job)
    pcall(reskate.animation.stop)
    if job.pass and job.keep_stage ~= false and job.phase ~= "wb_select" and job.pass <= settings.count then
        -- between slots of the walk-through's board pass: stay on the stage (leaving closes the menu)
        if job.board and job.board.opened then job.stage_open = true end
        return
    end
    if (job.board and job.board.opened) or job.stage_open then
        job.stage_open = false
        if job.board then job.board.opened = false end
        pcall(reskate.room.leave)
    end
end

local finish_snap -- below

-- ---- moving between presets with the app's help -----------------------------------------------
-- Plugins can hold a stick but not press a button, and presets move with the shoulder buttons
-- (Z and C on the keyboard). So the plugin asks Preset Wardrobe (the app) to press the key, through
-- snaps/keys.json, and waits for what you're wearing to change. No answer from the app (it isn't
-- running): you move yourself, as before.
local KEY_ANSWER_MS = 3000   -- the app answers in this long, or it isn't there
local CHANGE_MS = 1800       -- after a press, the outfit changes in this long, or it's pressed again
local key_seq = reskate.storage.get("key_seq") or 0

local function ask_key(job, key, on_moved, on_manual)
    key_seq += 1
    reskate.storage.set("key_seq", key_seq)
    reskate.files.write("snaps/keys.json", reskate.json.encode({id = key_seq, key = key}))
    job.adv = {id = key_seq, key = key, before = table.concat(worn_keys(), "|"), asked = now(),
        tries = (job.adv and job.adv.key == key and job.adv.retry) and job.adv.tries + 1 or 1,
        on_moved = on_moved, on_manual = on_manual}
    enter("advance")
end

local function read_key_answer()
    local text = reskate.files.read("snaps/keys_done.json")
    if not text then return nil end
    local ok, ans = pcall(reskate.json.decode, text)
    if ok and type(ans) == "table" then return ans end
    return nil
end

local function advance_step(job)
    local a = job.adv
    local t = now()
    if table.concat(worn_keys(), "|") ~= a.before then
        job.adv = nil
        return a.on_moved(true)
    end
    local ans = read_key_answer()
    local mine = ans and ans.id == a.id
    if mine and ans.pressed then
        a.pressed_at = a.pressed_at or t
        if t - a.pressed_at > CHANGE_MS then
            if a.tries < 3 then
                a.retry = true
                return ask_key(job, a.key, a.on_moved, a.on_manual)
            end
            -- pressed and nothing changed: two presets alike, most likely; take it as moved
            job.adv = nil
            return a.on_moved(false)
        end
    elseif mine and not ans.pressed then
        a.waiting = ans.why or "waiting" -- the game isn't in front: the banner says so
        if t - a.asked > 30000 then job.adv = nil; return a.on_manual() end
    elseif t - a.asked > KEY_ANSWER_MS then
        job.adv = nil
        job.manual = true -- no app: the player moves for the rest of this walk-through
        say("Preset Wardrobe (the app) didn't answer, so move to each preset yourself.", "warn")
        return a.on_manual()
    end
end

-- True when a controller is connected (gamepad() is nil with none, or when the game lacks focus).
local function pad_connected()
    if type(reskate.input.gamepad) ~= "function" then return true end -- older game: can't tell, so try the turn
    local ok, pad = pcall(reskate.input.gamepad)
    return not ok or pad ~= nil
end

-- One step of the job, every tick.
local function step()
    if not job then return end
    local elapsed = (now() - job.since) / 1000
    local menu_up = on_skaters_page()

    if job.phase == "wait_menu" then
        if menu_up then enter("hold") end
        return
    end

    if job.phase == "wb_select" then
        if not menu_up then
            job.stage_open = false -- the menu's closed already
            job.keep_stage = false
            return stop_job("Board pictures stopped. The outfits are saved; Preset Wardrobe adds them to your Fit Catalogue.", "warn")
        end
        return
    end
    -- Left the Skaters page during the board pictures: keep the fit without them.
    if not menu_up and string.sub(job.phase, 1, 6) == "board_" and job.board then
        reskate.capture.discard()
        job.board.opened = false -- the menu's closed already: leaving the stage would open it again
        end_board(job)
        return finish_snap(job, nil)
    end
    -- Left the Skaters page: drop any capture and wait to come back to the same preset.
    if not menu_up and job.phase ~= "saving" and string.sub(job.phase, 1, 6) ~= "board_" then
        if job.phase == "warm" or job.phase == "record" then reskate.capture.discard() end
        enter("wait_menu")
        say(string.format("Paused. Open the Skaters page on preset %d to carry on.", job.n), "warn")
        return
    end

    if job.phase == "advance" then
        if not menu_up then
            job.adv = nil
            return stop_job("Walk-through stopped: the Skaters page closed. The slots done so far are saved.", "warn")
        end
        return advance_step(job)
    end

    if job.phase == "move" then
        if elapsed >= settings.move then enter("hold") end

    elseif job.phase == "hold" then
        local turning = (settings.spin_deg or 0) > 0
        local front = true
        -- The turn is made with a stand-in pad stick, which the game ignores with no controller on.
        -- So without one the pictures are still shots, and the player is told how to get the full view.
        if turning and not pad_connected() then
            turning = false
            job.can_turn = false
            if not job.no_pad_told then
                job.no_pad_told = true
                say("No controller on, so this is still pictures only. Plug in a controller and snap again to get the full turn view.", "warn")
            end
        end
        if turning then
            local r = steer_to(0)
            job.can_turn = r ~= nil
            front = r ~= false -- nil: no reading, so no turn either
        end
        job.facing = front
        if elapsed >= settings.hold_s and (front or elapsed >= settings.hold_s + 4) then
            -- Warm-up capture first: the game's depth is only copied once frames are being taken
            -- (what Fit Check found), so the real capture starts straight after it.
            local ok, why = reskate.capture.start(capture_options(0))
            if not ok then return stop_job("Could not start the picture: " .. tostring(why), "warn") end
            job.worn = worn_keys()
            local sl, par, cnt = skater_slots()
            if cnt >= 20 then job.slots, job.params = sl, par end -- what is being photographed, for the app to file it as
            if job.mode == "fit" then
                fit_seq += 1
                job.seq = fit_seq
                reskate.storage.set("fit_seq", fit_seq)
            end
            enter("warm")
        end

    elseif job.phase == "warm" then
        if job.can_turn then steer_to(0) end
        local status = reskate.capture.state()
        if status.state == "failed" then return stop_job("Capture failed: " .. tostring(status.message), "warn") end
        if (status.taken or 0) >= 20 then
            local ok, why = reskate.capture.start(capture_options(2))
            if not ok then return stop_job("Could not start the picture: " .. tostring(why), "warn") end
            job.turn = if job.can_turn and (settings.spin_deg or 0) > 0 then math.rad(settings.spin_deg) else 0
            job.travel, job.last_h, job.track = 0, heading(), {}
            enter("record")
        elseif elapsed > 10 then
            return stop_job("No frames came in for the picture. Keep the game window visible and try again.", "warn")
        end

    elseif job.phase == "record" then
        local status = reskate.capture.state()
        if status.state == "failed" then return stop_job("Capture failed: " .. tostring(status.message), "warn") end
        local kept = reskate.capture.kept()
        local ready
        if job.turn > 0 then
            -- Turn one way, adding up how far it has gone, until it has gone the whole way round
            -- (or the set angle). The heading wraps at 180, so it's the steps that are added.
            local h = heading()
            if h ~= nil and job.last_h ~= nil then
                local d = wrap_angle(h - job.last_h)
                if math.abs(d) < math.pi / 3 then job.travel += d end
            end
            if h ~= nil then job.last_h = h end
            local gone = math.abs(job.travel)
            table.insert(job.track, {frame = status.taken or 0, travel = gone})
            local left = job.turn - gone
            if left > math.rad(1) and h ~= nil and elapsed < 15 then
                local speed = math.clamp(SPIN_SPEED * left / math.rad(25), 0.15, SPIN_SPEED)
                reskate.room.turn(speed * (settings.turn_sign or 1))
                ready = false
            else
                ready = true -- all the way round, or the reading stopped: keep what's there
            end
        else
            ready = #kept >= 3
        end
        if ready and #kept >= 1 then
            if job.turn > 0 then reskate.room.turn(0) end
            local base = base_for(job)
            job.queue = {}
            if job.turn > 0 and #job.track > 1 then
                local gone = job.track[#job.track].travel
                local full = gone >= 2 * math.pi - math.rad(5)
                local count = if full then FRAMES_PER_TURN else math.max(2, math.ceil(math.deg(gone) / 15) + 1)
                -- The cover first, full size; then the frames, front-facing first.
                table.insert(job.queue, {frame = frame_at(job.track, kept, math.min(math.rad(COVER_DEG), gone)),
                    name = base .. ".png", size = COVER_SIZE, cover = true})
                -- The turn's frames are saved together, in one animated PNG: ReSkate frames a saved
                -- picture on the outline of what's in it, and saved together they share one frame,
                -- so the skater stays put and the same size. The app splits them apart.
                local frames = {}
                for i = 0, count - 1 do
                    -- A full turn's last frame would be the front again, so it stops one short.
                    local angle = if full then i * 2 * math.pi / count else i * gone / (count - 1)
                    table.insert(frames, frame_at(job.track, kept, angle))
                end
                table.insert(job.queue, {frames = frames, format = "apng", name = base .. "-turn.png", size = FRAME_SIZE, turn = true})
            else
                table.insert(job.queue, {frame = kept[#kept], name = base .. ".png", size = COVER_SIZE, cover = true})
            end
            job.files = {}
            enter("saving")
        elseif elapsed > 20 then
            return stop_job("The picture took too long. Try again.", "warn")
        end

    elseif job.phase == "saving" then
        local status = reskate.capture.state()
        -- One picture at a time: the next is saved once the last one is written.
        local this_saved = job.saving_now ~= nil and status.state == "saved"
            and (status.file == nil or string.find(status.file, job.saving_now, 1, true) ~= nil)
        if job.saving_now == nil or (this_saved and job.files[#job.files] ~= job.saving_now) then
            if job.saving_now then table.insert(job.files, job.saving_now) end
            local nextone = table.remove(job.queue, 1)
            if nextone then
                local ok, why = reskate.capture.save("snaps/" .. nextone.name, {frames = nextone.frames or {nextone.frame},
                    format = nextone.format or "png", fps = 12,
                    size = nextone.size or COVER_SIZE, background = "transparent", cutout = "auto", solid = false})
                if not ok then return stop_job("Could not save the picture: " .. tostring(why), "warn") end
                job.saving_now = nextone.name
                job.since = now()
                return
            end
        end
        if job.saving_now ~= nil and #job.queue == 0 and job.files[#job.files] == job.saving_now then
            reskate.capture.discard()
            -- Back to the front after a one-fit snap. Not in a walk-through: turning holds a stand-in
            -- pad in place of the player's controller, and they're moving to the next preset; each
            -- slot faces front at its own countdown instead.
            if job.turn > 0 and job.mode ~= "all" then turn_back() end
            job.main = job.files[1] -- the cover is saved first
            job.turn_file = job.files[2] -- the turn, in one file (nil for a single picture)
            job.depth = status.depth == true
            if job.mode == "fit" and settings.board_pictures and board_supported() and not job.no_board then
                job.board = {}
                enter("board_front")
                return
            end
            return finish_snap(job, nil)
        elseif status.state == "failed" then
            reskate.capture.discard()
            return stop_job("The picture could not be saved: " .. tostring(status.message), "warn")
        elseif elapsed > 30 then
            return stop_job("Saving the picture timed out.", "warn")
        end

    elseif job.phase == "board_front" then
        -- Your skater on the stage faces the way the preview does: front first, as the angle was set.
        -- Close enough not to show on the board; at its slowest the stick can nudge past a tighter
        -- target and back for seconds (6 s in a recording, 2026-11-02).
        local r = steer_to(0, math.rad(1))
        if r ~= false or elapsed > 2.5 then
            reskate.room.turn(0)
            enter("board_open")
        end

    elseif job.phase == "board_open" then
        local b = job.board
        if not b.asked then
            local ok, good, why = pcall(reskate.room.open)
            if not ok or not good then
                say("Board pictures skipped: " .. tostring(if ok then why else good), "warn")
                return finish_snap(job, nil)
            end
            b.asked = true
        end
        local st, why = reskate.room.state()
        if st == "open" then
            b.opened = true
            job.stage_open = job.pass ~= nil
            enter("board_views")
        elseif st == "failed" or elapsed > 10 then
            say("Board pictures skipped: the stage didn't open (" .. tostring(why or st) .. ").", "warn")
            end_board(job)
            return finish_snap(job, nil)
        end

    elseif job.phase == "board_views" then
        local ok, views, why, detail = pcall(make_board_views)
        if ok and views == nil and why == "wait" and elapsed < 15 then return end -- the game is still reading its animations
        if not ok or views == nil then
            say("Board pictures skipped: " .. tostring(if ok then (detail or why) else views), "warn")
            end_board(job)
            return finish_snap(job, nil)
        end
        job.board.views, job.board.k, job.board.marks = views, 1, {}
        local good, why2 = show_board_view(job)
        if not good then
            say("Board pictures skipped: " .. tostring(why2), "warn")
            end_board(job)
            return finish_snap(job, nil)
        end
        local started, why3 = reskate.capture.start(board_capture(0))
        if not started then
            say("Board pictures skipped: " .. tostring(why3), "warn")
            end_board(job)
            return finish_snap(job, nil)
        end
        enter("board_warm")

    elseif job.phase == "board_warm" then
        local status = reskate.capture.state()
        if status.state == "failed" or elapsed > 10 then
            reskate.capture.discard()
            say("Board pictures skipped: no frames came in.", "warn")
            end_board(job)
            return finish_snap(job, nil)
        end
        if (status.taken or 0) >= 20 then
            reskate.capture.start(board_capture(2))
            enter("board_record")
        end

    elseif job.phase == "board_record" then
        local status = reskate.capture.state()
        local b = job.board
        if status.state == "failed" or elapsed > 10 then
            reskate.capture.discard()
            say("Board pictures skipped: the capture stopped.", "warn")
            end_board(job)
            return finish_snap(job, nil)
        end
        local waited = now() - job.since
        local ready = waited >= BOARD_WAIT_MS
        if not ready and reskate.capture.compare then
            -- watch frame to frame since this view was asked for: it has shown once a frame differs
            -- from the one before it (the board turned), and is ready once frames stop changing
            local kept = reskate.capture.kept()
            local cur, before = kept[#kept], kept[#kept - 1]
            if cur and before and cur ~= b.seen and (b.k_from == nil or before >= b.k_from) then
                local d = reskate.capture.compare(before, cur)
                if d >= 0 then
                    b.seen = cur
                    b.frames = (b.frames or 0) + 1
                    if d > (b.top or 0) then b.top = d end
                    if d >= VIEW_CHANGED then b.moved = true; b.still = 0
                    elseif d <= VIEW_STILL then b.still = (b.still or 0) + 1 end
                end
            end
            ready = waited >= BOARD_SETTLE_MS and (b.moved or b.k == 1) and (b.still or 0) >= 2
            if not b.moved and b.k > 1 and not b.retried and waited >= BOARD_RETRY_MS then
                b.retried = true
                reskate.log(string.format("PresetWardrobe: board view %d didn't show, asking again", b.k))
                show_board_view(job)
            end
        elseif not ready and waited >= 500 and not reskate.capture.compare then
            ready = true
        end
        if ready then
            if waited >= BOARD_WAIT_MS then
                reskate.log(string.format("PresetWardrobe: board view %d taken at the time limit", b.k))
            end
            reskate.log(string.format("PresetWardrobe: board view %d after %d ms (%d frames, biggest change %.2f)", b.k, waited, b.frames or 0, b.top or 0))
            b.moved, b.still, b.frames, b.top, b.retried = nil, 0, nil, nil, nil
            local kept = reskate.capture.kept()
            table.insert(b.marks, kept[#kept] or status.taken or 0)
            b.k_from = kept[#kept]
            b.k += 1
            if b.k > 4 then
                enter("board_pick")
            else
                show_board_view(job)
                job.since = now()
            end
        end

    elseif job.phase == "board_pick" then
        local status = reskate.capture.state()
        local b = job.board
        if (status.taken or 0) >= b.marks[4] + 6 or elapsed > 2 then
            local kept = reskate.capture.kept()
            local base = base_for(job)
            b.queue, b.files = {}, {}
            local frames = {}
            for _, m in b.marks do
                local pick, d = kept[#kept], math.huge
                for _, k in kept do if math.abs(k - m) < d then pick, d = k, math.abs(k - m) end end
                table.insert(frames, pick)
            end
            -- the 4 views together, so they share one frame like the turn does
            table.insert(b.queue, {frames = frames, name = base .. "-board.png"})
            b.saving = nil
            enter("board_saving")
        end

    elseif job.phase == "board_saving" then
        local status = reskate.capture.state()
        local b = job.board
        local done = b.saving and status.state == "saved" and (status.file == nil or string.find(status.file, b.saving, 1, true) ~= nil)
        if b.saving == nil or done then
            if done then table.insert(b.files, b.saving); b.saving = nil end
            local nextone = table.remove(b.queue, 1)
            if nextone then
                local ok, why = reskate.capture.save("snaps/" .. nextone.name, {frames = nextone.frames, format = "apng", fps = 2,
                    size = BOARD_SIZE, background = "transparent", cutout = "auto", solid = false})
                if ok then
                    b.saving = nextone.name
                    job.since = now()
                    return
                end
                say("Board pictures skipped: " .. tostring(why), "warn")
                b.files = {}
            end
            reskate.capture.discard()
            end_board(job)
            return finish_snap(job, b.files[1])
        elseif status.state == "failed" or elapsed > 10 then
            reskate.capture.discard()
            say("Board pictures skipped: saving failed.", "warn")
            end_board(job)
            return finish_snap(job, nil)
        end
    end
end

-- A snap's pictures are all saved: file it (and its board views, when there are 4).
finish_snap = function(job, board)
    do
        if job.pass then
            if board and run_shots[job.pass] then
                run_shots[job.pass].board_turn = board
                write_index()
            end
            job.pass += 1
            if not board or job.pass > settings.count then
                job.keep_stage = false
                end_board(job)
                enter("done")
                write_index()
                return stop_job(if board then string.format("All %d slots and their boards snapped! Preset Wardrobe adds them to your Fit Catalogue.", settings.count)
                    else "Board pictures stopped. The outfits are saved; Preset Wardrobe adds them to your Fit Catalogue.",
                    if board then "info" else "warn")
            end
            job.board = nil
            local function next_board()
                job.board = {opened = job.stage_open}
                job.n = job.pass
                enter("board_front")
            end
            if not job.manual then
                return ask_key(job, "c", next_board, function()
                    say(string.format("Board %d saved. Move to preset %d, then %s.", job.pass - 1, job.pass, snap_hint()))
                    enter("wb_select")
                end)
            end
            say(string.format("Board %d saved. Move to preset %d, then %s.", job.pass - 1, job.pass, snap_hint()))
            return enter("wb_select")
        end
        local main, turn, depth = job.main, job.turn_file, job.depth
        if job.mode == "boardtest" then
            return stop_job(if board then string.format("Board test saved as snaps/test-board.png, all 4 views (angle %.1f°). OPEN SNAPS FOLDER to look.", settings.board_turn2 or -10)
                else "The board test didn't finish.", if board then "info" else "warn")
        end
        do
            if job.test then
                return stop_job("Test picture saved as snaps/" .. main .. ". OPEN SNAPS FOLDER to check the framing and turn.")
            end
            if job.mode == "fit" then
                table.insert(singles, {seq = job.seq, request = job.request, file = main, turn = turn, board_turn = board, worn = job.worn,
                    at = wall_clock(), depth = depth, slots = job.slots, params = job.params})
                while #singles > MAX_SINGLES do
                    local old = table.remove(singles, 1)
                    if type(old) == "table" then
                        pcall(reskate.files.remove, "snaps/" .. tostring(old.file))
                        for _, f in (old.frames or {}) do pcall(reskate.files.remove, "snaps/" .. tostring(f)) end
                        for _, f in (old.board or {}) do pcall(reskate.files.remove, "snaps/" .. tostring(f)) end
                        if old.turn then pcall(reskate.files.remove, "snaps/" .. tostring(old.turn)) end
                        if old.board_turn then pcall(reskate.files.remove, "snaps/" .. tostring(old.board_turn)) end
                    end
                end
                reskate.storage.set("singles", singles)
                write_index()
                return stop_job(if board then "Fit and board snapped! Preset Wardrobe adds it to your Fit Catalogue."
                    else "Fit snapped! Preset Wardrobe adds it to your Fit Catalogue.")
            end
            run_shots[job.n] = {n = job.n, file = main, turn = turn, board_turn = board, worn = job.worn, at = wall_clock(),
                depth = depth}
            write_index()
            if job.n >= settings.count then
                write_index()
                if settings.board_pictures ~= false and board_supported() then
                    -- The board pictures show your own skater on the stage, so they come in a
                    -- second pass: back to the first preset, then a board for each.
                    job.pass, job.board = 1, nil
                    if job.manual then
                        say(string.format("All %d outfits snapped. Now the boards: move to preset 1, then %s.", settings.count, snap_hint()))
                        return enter("wb_select")
                    end
                    job.rewind = settings.count - 1
                    local function back()
                        if job.rewind <= 0 then
                            job.board = {opened = job.stage_open}
                            job.n = job.pass
                            return enter("board_front")
                        end
                        job.rewind -= 1
                        return ask_key(job, "z", back, function() enter("wb_select") end)
                    end
                    return back()
                end
                enter("done")
                write_index()
                turn_back() -- the walk-through's over: back to the front
                return stop_job(string.format("All %d slots snapped! Preset Wardrobe adds them to your Fit Catalogue.", settings.count))
            end
            job.n += 1
            job.saving_now, job.queue, job.files, job.board = nil, nil, nil, nil
            if job.manual then return enter("move") end
            return ask_key(job, "c", function() enter("hold") end, function() enter("move") end)
        end
    end
end

-- ---- titles bridge: PresetBanks asks for item names it can't read itself ----------------------
local wanted_text = nil
local function answer_titles()
    local text = reskate.files.read("wanted.json")
    if not text or text == wanted_text then return end
    wanted_text = text
    local ok, keys = pcall(reskate.json.decode, text)
    if not ok or type(keys) ~= "table" then return end
    local titles = {}
    local n = 0
    for _, key in keys do
        if type(key) == "string" and #key > 0 and #key <= 255 and n < 2000 then
            titles[key] = reskate.outfit.title(key)
            n += 1
        end
    end
    reskate.files.write("titles.json", reskate.json.encode(titles))
end
reskate.time.every(5000, function() pcall(answer_titles) end)

-- Preset Wardrobe writes added.json when it files a snap; say so in the game.
local acked = reskate.storage.get("acked") or 0
local function read_ack()
    local text = reskate.files.read("added.json")
    if not text then return end
    local ok, ack = pcall(reskate.json.decode, text)
    if not ok or type(ack) ~= "table" or type(ack.messages) ~= "table" then return end
    local newest = acked
    for _, m in ack.messages do
        if type(m) == "table" and type(m.id) == "number" and m.id > acked then
            if type(m.message) == "string" and m.message ~= "" then
                reskate.ui.toast(string.sub(m.message, 1, 200), if m.warn then "warn" else "info")
            end
            newest = math.max(newest, m.id)
        end
    end
    if newest ~= acked then
        acked = newest
        reskate.storage.set("acked", acked)
    end
end
reskate.time.every(1500, function() pcall(read_ack) end)

-- ---- picture requests from the app ("Update picture"), listed under Messages ------------------------
-- requests.json: {"requests": [{id, name, slot}]}. Taking one snaps the selected preset as usual,
-- marked with the request, so the app gives the new pictures to that fit (and drops the request).
-- They show at the top of the plugin's page.
-- Dismissed ones go back in requests_dismissed.json.
local requests = {}
local seen_requests = reskate.storage.get("seen_requests") or 0
local dismissed = {}
local function read_requests()
    local text = reskate.files.read("requests.json")
    if not text then requests = {} return end
    local ok, data = pcall(reskate.json.decode, text)
    if not ok or type(data) ~= "table" or type(data.requests) ~= "table" then return end
    local list, newest = {}, seen_requests
    for _, r in data.requests do
        if type(r) == "table" and type(r.id) == "number" and not dismissed[r.id] then
            table.insert(list, {id = r.id, name = tostring(r.name or "A fit"), slot = tonumber(r.slot) or 0})
            if r.id > seen_requests then
                reskate.ui.toast(string.sub(string.format("Preset Wardrobe: %s needs a new picture. See the plugin's page (Insert > PLUGINS).", tostring(r.name or "A fit")), 1, 200), "info")
                newest = math.max(newest, r.id)
            end
        end
    end
    requests = list
    if newest ~= seen_requests then
        seen_requests = newest
        reskate.storage.set("seen_requests", seen_requests)
    end
end
if PICTURE_REQUESTS then reskate.time.every(1500, function() pcall(read_requests) end) end

local function dismiss_request(id)
    dismissed[id] = true
    local ids = {}
    for k in dismissed do table.insert(ids, k) end
    reskate.files.write("requests_dismissed.json", reskate.json.encode({ids = ids}))
    pcall(read_requests)
end

-- At the top of the page while any are waiting: one card each.
local function messages_page(ui)
    for i, r in requests do
        ui:card(if i == 1 then string.format("MESSAGES (%d)", #requests) else "MESSAGE",
            string.sub(r.name .. " needs a new picture", 1, 100))
        if r.slot > 0 then
            ui:label(string.format("On the Skaters page, go to preset %d, then press Take it now.", r.slot))
        else
            ui:warn("It isn't in the game right now: load a rotation with it in the app first.")
        end
        if job then
            ui:info("Now", "snapping...")
        else
            if ui:button("take_" .. r.id, "Take it now", true) then
                start_job("fit")
                if job then job.request = r.id end
            end
            if ui:button("dismiss_" .. r.id, "Dismiss") then dismiss_request(r.id) end
        end
    end
    ui:note("Take it now snaps the preset you have selected, as " .. snap_key() .. " does. Changed the outfit or board since? Then it's added as its own fit.")
end
reskate.time.after(2000, function() pcall(answer_titles) end)

local stick_test_step -- the stick test, below
local cal_step -- board calibration, below
reskate.on("tick", function()
    step()
    if stick_test_step then stick_test_step() end
    if cal_step then cal_step() end
    -- Back to the front after a snap, unless a snap is steering it already.
    if homing_until and job == nil then
        if now() > homing_until or not on_skaters_page() or steer_to(0) ~= false then
            reskate.room.turn(0)
            homing_until = nil
        end
    end
end)

-- ---- board angle calibration: one view shown live on the stage --------------------------------
local cal = nil -- {k = view, phase = "open" | "show", redo_at}
local function cal_show(k)
    if job then return say("Finish or cancel the snap first.", "warn") end
    if not on_skaters_page() then return say("Open the Skaters page first.", "warn") end
    if not cal then
        local ok, good, why = pcall(reskate.room.open)
        if not ok or not good then return say("Couldn't open the stage: " .. tostring(if ok then why else good), "warn") end
        cal = {phase = "open", since = now()}
    end
    cal.k = k
    if cal.phase == "show" then cal.redo_at = now() end
end
local function cal_done()
    if not cal then return end
    cal = nil
    pcall(reskate.animation.stop)
    pcall(reskate.room.leave)
end
cal_step = function()
    if not cal then return end
    if cal.phase == "open" then
        local st = reskate.room.state()
        if st == "open" then
            cal.phase, cal.redo_at = "show", now()
        elseif st == "failed" or now() - cal.since > 10000 then
            cal = nil
            say("Couldn't open the stage.", "warn")
        end
        return
    end
    if cal.redo_at and now() >= cal.redo_at then
        cal.redo_at = nil
        local ok, views, why, detail = pcall(make_board_views)
        if not ok or not views then
            if ok and why == "wait" then cal.redo_at = now() + 500 return end
            cal_done()
            return say("Couldn't show the board: " .. tostring(if ok then (detail or why) else views), "warn")
        end
        pcall(reskate.animation.play, views[cal.k], {paused = true, at = 0, blend = 0, loop = true})
    end
end

-- ---- test: can the plugin move between presets? (F9 right, F10 left) --------------------------------
-- Holds the stand-in pad's left stick over for a moment, then watches what you're wearing for 2 s
-- and reports how many times it changed. Test only.
local STICK_PUSH = 0.9
local stick_test = nil
local function start_stick_test(dir)
    if job then return say("Finish or cancel the snap first.", "warn") end
    if not on_skaters_page() then return say("Open the Skaters page first.", "warn") end
    local held = settings.stick_ms or 300
    stick_test = {dir = dir, since = now(), held = held, last = table.concat(worn_keys(), "|"), changes = 0}
end
stick_test_step = function()
    local t = stick_test
    if not t then return end
    local age = now() - t.since
    if age < t.held then
        reskate.room.turn(STICK_PUSH * t.dir, "left_x")
    elseif not t.released then
        reskate.room.turn(0, "left_x")
        t.released = true
    end
    local worn = table.concat(worn_keys(), "|")
    if worn ~= t.last then
        t.changes += 1
        t.last = worn
        t.first_at = t.first_at or age
    end
    if age > t.held + 2000 then
        stick_test = nil
        local msg = if t.changes == 0 then string.format("Stick test (%s, %d ms): nothing changed.", if t.dir > 0 then "right" else "left", t.held)
            else string.format("Stick test (%s, %d ms): what you're wearing changed %d time%s (first after %d ms).",
                if t.dir > 0 then "right" else "left", t.held, t.changes, if t.changes == 1 then "" else "s", t.first_at)
        reskate.log("PresetWardrobe: " .. msg)
        say(msg)
    end
end
if DEV_TOOLS then
    reskate.input.bind("presetwardrobe_stick_right", "F9")
    reskate.input.bind("presetwardrobe_stick_left", "F10")
end

-- ---- the snap key and the controller's hold ------------------------------------------------------
local function snap_pressed()
    if job and job.phase == "wb_select" then
        job.board = {opened = job.stage_open}
        job.n = job.pass -- the board's pictures are named after its slot
        return enter("board_front")
    end
    start_job("fit")
end
-- Which key the snap is on, seen as it's pressed: the keys held when the action comes (virtual keys: F1-F12, A-Z,
-- 0-9, with Ctrl, Shift and Alt). Written to snap_key_seen.json for the app, which can't always read the bind
-- from the player's profile.
local VK_NAMES = {}
for i = 1, 12 do VK_NAMES[0x6F + i] = "F" .. i end
for c = 0x41, 0x5A do VK_NAMES[c] = string.char(c) end
for c = 0x30, 0x39 do VK_NAMES[c] = string.char(c) end
local seen_key = nil
local function key_down(vk)
    local ok, v = pcall(reskate.input.key, vk)
    return ok and (v == true or (type(v) == "number" and v ~= 0))
end
local function note_snap_key()
    if type(reskate.input.key) ~= "function" then return end
    local main
    for vk = 0x70, 0x7B do if key_down(vk) then main = VK_NAMES[vk] break end end
    if not main then
        for vk = 0x30, 0x5A do
            if VK_NAMES[vk] and key_down(vk) then main = VK_NAMES[vk] break end
        end
    end
    if not main then return end -- a controller combo, or the key already let go
    local key = main
    if key_down(0x12) then key = "Alt+" .. key end
    if key_down(0x10) then key = "Shift+" .. key end
    if key_down(0x11) then key = "Ctrl+" .. key end
    if key == seen_key then return end
    seen_key = key
    pcall(reskate.files.write, "snap_key_seen.json", reskate.json.encode({key = key, at = wall_clock()}))
end
reskate.input.bind(SNAP_ACTION, "F8")
reskate.on("action", function(name)
    if name == "presetwardrobe_stick_right" then return start_stick_test(1) end
    if name == "presetwardrobe_stick_left" then return start_stick_test(-1) end
    if name == SNAP_ACTION then
        pcall(note_snap_key)
        return snap_pressed()
    end
end)

-- Holding Y on the Skaters page for PAD_HOLD_MS snaps (or cancels), once per hold: let go to do it again.
local card = nil -- the Skaters page card (set further down): {slot, kind, name, parts}
local pad_hold = {since = nil, fired = false}
reskate.on("tick", function()
    if type(reskate.input.gamepad) ~= "function" then return end
    local ok, pad = pcall(reskate.input.gamepad)
    local down = ok and type(pad) == "table" and type(pad.buttons) == "table" and pad.buttons[PAD_BUTTON] == true
    if not down then
        pad_hold.since, pad_hold.fired = nil, false
        return
    end
    if pad_hold.fired then return end
    local okp, onp = pcall(on_skaters_page)
    local picking = false -- an item grid open (picking clothes): Y may be the game's there
    if type(reskate.menu.grid) == "function" then
        local okg, g = pcall(reskate.menu.grid)
        picking = okg and g ~= nil
    end
    -- Y only snaps a preset that isn't in the Fit Catalogue yet, or one changed since it was saved (the card says
    -- which); on a saved, unchanged one it does nothing. During a snap it cancels.
    local wanted = job ~= nil or (card ~= nil and (card.kind == "unsaved" or card.kind == "nopics"))
    if not (okp and onp) or picking or not wanted then
        pad_hold.since = nil
        return
    end
    pad_hold.since = pad_hold.since or now()
    if now() - pad_hold.since >= PAD_HOLD_MS then
        pad_hold.fired, pad_hold.since = true, nil
        snap_pressed()
    end
end)

-- ---- what's drawn over the game ------------------------------------------------------------------
local function v(x, y) return vector.create(x, y, 0) end

local function menu_rect(d)
    local size = d:size()
    local w = math.min(size.x, size.y * 21 / 9)
    local left = (size.x - w) / 2
    local a = settings.area
    return v(left + a[1] * w, a[2] * size.y), v(left + a[3] * w, a[4] * size.y)
end

reskate.on("draw", function(d)
    local size = d:size()
    if size.x <= 0 or size.y <= 0 then return end
    local k = size.y / 1080
    local framing_only = job == nil and settings.framing_preview and on_skaters_page()
    if not job and not framing_only then return end

    -- The framing box: where the picture is cut from.
    if framing_only or (job and (job.phase == "hold" or job.phase == "move")) then
        local tl, br = menu_rect(d)
        d:rect(tl, br, {colour = "#5ecdfaCC", thickness = 3, rounding = 6 * k})
    end
    if not job then return end

    local title, detail
    local count = settings.count
    local single = job.mode ~= "all"
    if single and job.phase == "wait_menu" then
        title = "OPEN THE SKATERS PAGE"
        detail = "Select the preset to snap. " .. cancel_hint()
    elseif single and job.phase == "hold" then
        local left = math.max(0, settings.hold_s - (now() - job.since) / 1000)
        title = string.format("%s  -  HOLD STILL  %d", if job.test then "TEST PICTURE" else "SNAPPING THIS FIT", math.ceil(left))
        detail = if left <= 0 and not job.facing then "Turning your skater to face front..." else "Keep this preset selected. " .. cancel_hint()
    elseif job.phase == "advance" then
        local a = job.adv
        title = if job.pass then (if job.rewind and job.board == nil and a and a.key == "z" then "BACK TO THE FIRST PRESET..." else string.format("ON TO BOARD %d OF %d...", job.pass, count))
            else string.format("ON TO PRESET %d OF %d...", job.n, count)
        detail = if a and a.waiting then "Click the game window so Preset Wardrobe can move to the next preset."
            else "Preset Wardrobe is moving to the next preset. Hands off."
    elseif job.phase == "wb_select" then
        title = string.format("BOARDS  -  MOVE TO PRESET %d OF %d, THEN %s", job.pass, count, string.upper(snap_hint()))
        detail = "Go to it on the Skaters page, then " .. snap_hint() .. " and keep your hands off for a moment."
    elseif job.pass and string.sub(job.phase, 1, 6) == "board_" then
        title = string.format("PICTURING BOARD %d OF %d...", job.pass, count)
        detail = "Hands off for a moment."
    elseif single and string.sub(job.phase, 1, 6) == "board_" then
        title = if job.board and job.board.k and job.phase == "board_record"
            then string.format("PICTURING THE BOARD  %d / 4", math.min(4, job.board.k)) else "PICTURING THE BOARD..."
        detail = "Hands off for a moment. The menu closes when it's done."
    elseif single then
        title = if job.test then "TAKING THE TEST PICTURE..." else "SNAPPING FIT..."
        detail = "Hands off for a moment."
    elseif job.phase == "wait_menu" then
        title = if job.n == 1 then "OPEN THE SKATERS PAGE" else "PAUSED"
        detail = if job.n == 1 then "Highlight your first (leftmost) preset. " .. cancel_hint()
            else string.format("Go back to the Skaters page on preset %d. %s", job.n, cancel_hint())
    elseif job.phase == "hold" then
        local left = math.max(0, settings.hold_s - (now() - job.since) / 1000)
        title = string.format("PRESET %d OF %d  -  HOLD STILL  %d", job.n, count, math.ceil(left))
        if left <= 0 and not job.facing then title = string.format("PRESET %d OF %d  -  FACING FRONT...", job.n, count) end
        detail = if job.test then "Test picture of the highlighted preset." else "Keep this preset highlighted. " .. cancel_hint()
    elseif job.phase == "move" then
        title = string.format("MOVE RIGHT  ->  PRESET %d", job.n)
        detail = string.format("Highlight preset %d now.", job.n)
    else
        title = string.format("SNAPPING PRESET %d...", job.n)
        detail = "Hands off for a moment."
    end

    local big = {font = "bold", size = 26, colour = "#F5F5F5FF"}
    local small = {size = 18, colour = "#C8CCD2FF"}
    local w1 = d:measure(title, big).x
    local w2 = d:measure(detail, small).x
    local w = math.max(w1, w2, 360 * k) + 64 * k
    local h = 96 * k
    local left, top = (size.x - w) / 2, 24 * k
    d:rect(v(left, top), v(left + w, top + h), {colour = "#141518E6", fill = true, rounding = 8 * k})
    d:text(v(size.x / 2, top + 14 * k), title, {font = "bold", size = 26, colour = "#F5F5F5FF", align = "centre"})
    d:text(v(size.x / 2, top + 50 * k), detail, {size = 18, colour = "#C8CCD2FF", align = "centre"})

    -- Progress dots
    if job.mode == "all" then
        local gap = 18 * k
        local x0 = size.x / 2 - (count - 1) * gap / 2
        for i = 1, count do
            local done = run_shots[i] ~= nil
            local colour = if i == job.n then "#5ecdfaFF" elseif done then "#7BD88FFF" else "#4A4E57FF"
            d:circle(v(x0 + (i - 1) * gap, top + h - 14 * k), 5 * k, {colour = colour, fill = true})
        end
    end
end)

-- ---- the plugin's page (Insert > PLUGINS > Preset Wardrobe) ---------------------------------------
-- The testing cards, off unless DEV_TOOLS.
local function dev_cards(ui)
    ui:card("TIMING", "How long the banner gives you")
    local hold = math.floor(ui:slider("hold", "Hold still for", settings.hold_s, 1, 8, "%d s") + 0.5)
    local move = math.floor(ui:slider("move", "Time to move to the next", settings.move, 1, 6, "%d s") + 0.5)
    local count = math.floor(ui:slider("count", "Slots in a walk-through", settings.count, 1, 10, "%d") + 0.5)
    if hold ~= settings.hold_s or move ~= settings.move or count ~= settings.count then
        settings.hold_s, settings.move, settings.count = hold, move, count
        save_settings()
    end

    ui:card("TURN", "Turns your skater while snapping, so the board shows and the app can spin the preview")
    local turn = math.floor(ui:slider("spin_deg", "Turn while snapping", settings.spin_deg or 0, 0, 360, "%d°") + 0.5)
    ui:note("360 spins all the way round, so the app can show backs and backpacks. Every snap starts and ends facing front. 0 takes one picture.")
    if turn ~= settings.spin_deg then
        settings.spin_deg = turn
        save_settings()
    end
    local h = heading()
    ui:info("Preview now", if h then string.format("%d° from front", math.floor(math.deg(h) + 0.5)) else "open the Skaters page to read it")
    if ui:button("face_front", "Turn the preview to face front") then turn_back() end

    ui:card("TEST: MOVING BETWEEN PRESETS", "Can the plugin move along the Skaters page by itself?")
    ui:label("On the Skaters page, press F9 to push right once, F10 to push left once.")
    local sms = math.floor(ui:slider("stick_ms", "How long it pushes", settings.stick_ms or 300, 100, 1000, "%d ms") / 50 + 0.5) * 50
    if sms ~= (settings.stick_ms or 300) then settings.stick_ms = sms; save_settings() end
    ui:note("A message says whether your preset changed, and how many times (more than once means it kept scrolling).")

    ui:card("BOARD ANGLE (CALIBRATE)", "Show a board view on the Skaters page and nudge the sliders until it's square")
    ui:label("On the Skaters page, pick a view. It updates as you move the sliders.")
    local views = {"Bottom", "Side", "Griptape", "Other side"}
    for k = 1, 4 do
        if ui:button("cal_" .. k, (if cal and cal.k == k then "> " else "") .. "Show " .. views[k]) then cal_show(k) end
    end
    local function knob(id, label, field, default)
        local v = math.floor(ui:slider(id, label, settings[field] or default, -25, 25, "%.1f°") * 4 + 0.5) / 4
        if v ~= (settings[field] or default) then
            settings[field] = v
            save_settings()
            if cal and cal.phase == "show" then cal.redo_at = now() + 250 end
        end
    end
    knob("board_turn2", "Board angle (turn)", "board_turn2", -10)
    knob("board_tilt_a2", "Tilt A", "board_tilt_a2", 0)
    knob("board_tilt_b2", "Tilt B", "board_tilt_b2", 0)
    if ui:button("cal_reset", "Reset the tilts to 0") then
        settings.board_tilt_a2, settings.board_tilt_b2 = 0, 0
        save_settings()
        if cal and cal.phase == "show" then cal.redo_at = now() end
    end
    if cal and ui:button("cal_done", "Done (leaves the stage and closes the menu)", true) then cal_done() end
    ui:note("Griptape: no wheels peeking out either side. Side: the deck a straight, upright line nose to tail. Send the three numbers once it looks right.")

    ui:card("BOARD", "4 pictures of the board after each snap: bottom, side, griptape, other side")
    local bp = ui:toggle("board_pictures", "Board pictures", settings.board_pictures ~= false,
        "Your skater holds the board up on the Skaters page while it's pictured, then the menu closes. Adds about 5 seconds.")
    if bp ~= (settings.board_pictures ~= false) then
        settings.board_pictures = bp
        save_settings()
    end
    if not job and ui:button("board_test", "Test board pictures (only the board, not added to the app)") then start_job("boardtest") end
    if not board_supported() then ui:warn("This ReSkate version can't do board pictures.") end

    ui:card("FRAMING (CALIBRATION)", "The blue box on the Skaters page is what gets pictured")
    local preview = ui:toggle("framing", "Show the framing box", settings.framing_preview == true,
        "Draws the box on the Skaters page so you can line it up around your skater.")
    local a = settings.area
    local l = ui:slider("left", "Left edge", a[1], 0, 0.9, "%.2f")
    local t = ui:slider("top", "Top edge", a[2], 0, 0.9, "%.2f")
    local r = ui:slider("right", "Right edge", a[3], 0.1, 1, "%.2f")
    local b = ui:slider("bottom", "Bottom edge", a[4], 0.1, 1, "%.2f")
    if r < l + 0.05 then r = math.min(1, l + 0.05) end
    if b < t + 0.05 then b = math.min(1, t + 0.05) end
    if preview ~= (settings.framing_preview == true) or l ~= a[1] or t ~= a[2] or r ~= a[3] or b ~= a[4] then
        settings.framing_preview = preview
        settings.area = {l, t, r, b}
        save_settings()
    end
    if ui:button("reset_area", "Reset framing") then
        settings.area = table.clone(DEFAULT_AREA)
        save_settings()
    end
    if ui:button("test", "Test picture (highlighted preset)") then
        if job then stop_job(nil) end
        start_job("test")
    end
    if ui:button("open", "Open snaps folder") then
        reskate.files.write("snaps/.keep", "")
        local ok, why = reskate.files.reveal("snaps")
        if not ok then say("Could not open the folder: " .. tostring(why), "warn") end
    end
    ui:note("Preset Wardrobe " .. VERSION .. ". Pictures come from the game's own preview, with the background cut out.")
end

reskate.menu.page("Preset Wardrobe", function(ui)
    if PICTURE_REQUESTS and #requests > 0 then messages_page(ui) end
    ui:card("SNAP A FIT", "Adds a preset to your Fit Catalogue in Preset Wardrobe")
    local hk = read_hotkeys()
    for _, c in ipairs(hk.conflicts) do
        if type(c) == "table" and type(c.name) == "string" then
            ui:warn(string.format("Warning: Preset Wardrobe shares its hotkey (%s) with %s. Please rebind it in ReSkate: Settings > Controls > Plugins' own binds.",
                tostring(c.key or snap_key()), c.name))
        end
    end
    ui:label("1. Close this menu and open the game's Skaters page.")
    ui:label("2. Select the preset you want to keep and press " .. snap_key() .. ".")
    ui:note("On a controller: hold " .. PAD_NAME .. " on the Skaters page instead.")
    ui:label("3. Hold still while it snaps. That's it.")
    if job then
        ui:info("Now", if job.mode == "all" then string.format("slot %d of %d", job.n, settings.count) else "snapping")
        if job.phase == "wb_select" and ui:button("skip_boards", "Skip the board pictures", true) then
            job.keep_stage = false
            end_board(job)
            enter("done")
            write_index()
            stop_job("Board pictures skipped. The outfits are saved.")
        end
        if job and ui:button("cancel", "Cancel snapping", true) then stop_job("Snapping cancelled.") end
    else
        if ui:button("start", "Snap the selected preset", true) then start_job("fit") end
    end
    ui:info("Last", last_message)

    ui:card("ALL 10 IN A ROW", "Snap every slot in one go")
    if WALKTHROUGH then
        ui:note("Go to your first (leftmost) preset and start. With the Preset Wardrobe app open it moves along by itself: hands off until it's done, and keep the game the window in front.")
        ui:note("Without the app, follow the banner: hold still, move one to the right, repeat.")
        if not job and ui:button("start_all", "Walk through all slots") then start_job("all") end
        local done = 0
        for i = 1, settings.count do if run_shots[i] then done += 1 end end
        ui:bar(string.format("%d / %d snapped this session", done, settings.count), done, 0, settings.count)
    else
        ui:info("Walk through all slots", "Not available yet")
        ui:note("NOTE: This is a work in progress and is switched off for now. Snap your presets one at a time until it's ready.")
    end

    if DEV_TOOLS then dev_cards(ui) end
    ui:note("Preset Wardrobe " .. VERSION)
end)

reskate.on("unload", function()
    if job then reskate.capture.discard() end
end)

-- Tells the app whether the Skaters page is open with no item grid in focus, so that after a live load
-- it can switch to the next preset and back, which makes the game redraw the whole skater.
local menu_next, menu_last, menu_wrote = 0, "", 0
reskate.on("tick", function()
    local t = reskate.time.now()
    if t < menu_next then return end
    menu_next = t + 250
    if type(reskate.menu.game) ~= "function" then return end
    local ok, m = pcall(reskate.menu.game)
    if not ok or type(m) ~= "table" then return end
    local grid = false
    if type(reskate.menu.grid) == "function" then
        local okg, g = pcall(reskate.menu.grid)
        grid = okg and g ~= nil
    end
    local text = string.format('{"known":%s,"open":%s,"skaters":%s,"grid":%s}',
        tostring(m.known == true), tostring(m.open == true), tostring(m.skaters == true), tostring(grid))
    if text ~= menu_last or t - menu_wrote > 1500 then
        menu_last, menu_wrote = text, t
        pcall(reskate.files.write, "menu_state.json", text)
    end
end)

-- Which items the game has installed. The app asks by writing item_check.json: {"id":N,"names":[...]}; the answer
-- goes to item_check_result.json: {"id":N,"ok":true,"boot":T,"missing":"name\nname"}. reskate.outfit.find looks
-- through the game's whole item catalog, so it also knows board parts. It answers nothing while the catalog can't be
-- read (no skater in the world), which looks the same as a missing item, so an item that is worn right now is
-- looked up first: if even that isn't found, ok is false and the app assumes nothing.
local check_id, check_next, check_boot, check_good = 0, 0, now(), nil
reskate.on("tick", function()
    local t = now()
    if t < check_next then return end
    check_next = t + 50
    local ok, text = pcall(reskate.files.read, "item_check.json")
    if not ok or type(text) ~= "string" or text == "" then return end
    local okd, c = pcall(reskate.json.decode, text)
    if not okd or type(c) ~= "table" or type(c.id) ~= "number" or c.id == check_id or type(c.names) ~= "table" then return end
    check_id = c.id
    local function answer(good, missing, why)
        pcall(reskate.files.write, "item_check_result.json", reskate.json.encode({
            id = c.id, ok = good, boot = check_boot, missing = table.concat(missing, "\n"), why = why or ""}))
    end
    if type(reskate.outfit) ~= "table" or type(reskate.outfit.find) ~= "function" then return answer(false, {}, "no outfit.find") end
    -- an item the skater really wears, from its slots (worn() lists only what plugins put on)
    local control
    local okw, why = pcall(function()
        for _, sl in ipairs(reskate.outfit.slots()) do
            if type(sl.item) == "string" and sl.item ~= "" then control = sl.item break end
        end
        if not control then
            for _, it in reskate.outfit.worn() do
                if type(it.item) == "string" and it.item ~= "" then control = it.item break end
            end
        end
    end)
    -- the skater's slots can read empty for a moment after a refresh: the last item that was found does as well
    control = control or check_good
    if not control then return answer(false, {}, "no worn item to test with" .. (okw and "" or (": " .. tostring(why)))) end
    local okc, found = pcall(reskate.outfit.find, control)
    if not okc then return answer(false, {}, "find failed: " .. tostring(found)) end
    if found == nil then return answer(false, {}, "the catalog doesn't know " .. control) end
    check_good = control
    local missing = {}
    for i, name in ipairs(c.names) do
        if i > 40 then break end
        if type(name) == "string" and name ~= "" then
            local okf, f = pcall(reskate.outfit.find, name)
            if okf and f == nil then table.insert(missing, name) end
        end
    end
    answer(true, missing, "")
end)

-- A black cover that fades in and out over the game, so the replay flashing by during a refresh isn't seen.
-- The app asks for it by writing cover.json: {"id":N,"in":ms,"hold":ms,"out":ms}.
local cover, cover_id, cover_next = nil, 0, 0
reskate.on("tick", function()
    local t = reskate.time.now()
    if t < cover_next then return end
    cover_next = t + 50
    if cover and cover.wait then
        -- released, but only once the skater wears what was loaded (or it has taken too long)
        local sl, _, cnt = skater_slots()
        local same, diff = cnt >= 20, {}
        if cnt >= 20 then
            for name, item in pairs(cover.wait.expect) do
                local now = string.lower(sl[name] or "")
                if now ~= item then
                    same = false
                    if #diff < 8 then table.insert(diff, name .. ": wanted " .. item .. ", has " .. (now == "" and "nothing" or now)) end
                end
            end
        else
            table.insert(diff, "the skater's slot list was empty (" .. cnt .. " slots)")
        end
        if same or t >= cover.wait.till then
            cover.hold = math.max(0, (t - cover.start) - cover.fin)
            pcall(reskate.files.write, "cover_done.json", reskate.json.encode({
                waited = t - cover.wait.since, matched = same, shown = (t - cover.start) + cover.fout, diff = diff}))
            cover.wait = nil
        end
    end
    local ok, text = pcall(reskate.files.read, "cover.json")
    if not ok or type(text) ~= "string" or text == "" then return end
    local okd, c = pcall(reskate.json.decode, text)
    if not okd or type(c) ~= "table" or type(c.id) ~= "number" or c.id == cover_id then return end
    cover_id = c.id
    if c.release then
        -- the app says the skater is back: end the hold now and fade out
        if cover then
            if type(c.expect) == "table" and next(c.expect) ~= nil then
                cover.wait = {expect = c.expect, till = t + (tonumber(c.late) or 1500), since = t}
            else
                local e = t - cover.start
                cover.hold = math.max(0, e - cover.fin)
                pcall(reskate.files.write, "cover_done.json", reskate.json.encode({waited = 0, matched = false, shown = e + cover.fout}))
            end
        end
        return
    end
    cover = {start = t, fin = tonumber(c["in"]) or 120, hold = tonumber(c.hold) or 300, fout = tonumber(c.out) or 250}
end)

reskate.on("draw", function(d)
    if not cover then return end
    local e = reskate.time.now() - cover.start
    local a
    if e < cover.fin then
        a = e / math.max(cover.fin, 1)
    elseif e < cover.fin + cover.hold then
        a = 1
    elseif e < cover.fin + cover.hold + cover.fout then
        a = 1 - (e - cover.fin - cover.hold) / math.max(cover.fout, 1)
    else
        cover = nil
        return
    end
    local size = d:size()
    local alpha = math.max(0, math.min(255, math.floor(a * 255 + 0.5)))
    d:rect(vector.create(0, 0, 0), vector.create(size.x, size.y, 0), {colour = string.format("#000000%02x", alpha), fill = true})
end)


-- What the skater wears right now, slot by slot (skin tone, head, body, hair, clothes, tattoos...), for the app to
-- compare with the preset it thinks is selected. reskate.outfit.slots() is the game's own list. It goes to
-- skater_now.json when it changes, and now and then even if it hasn't, so the app can tell the plugin is alive.
local now_next, now_last, now_wrote = 0, "", 0
reskate.on("tick", function()
    local t = reskate.time.now()
    if t < now_next then return end
    now_next = t + 1000
    local out, params, n = skater_slots()
    if n < 20 then return end -- the list is empty for a moment now and then (editor opening, a refresh)
    local text = reskate.json.encode({n = n, slots = out, params = params})
    if text ~= now_last or t - now_wrote > 1500 then
        now_last, now_wrote = text, t
        pcall(reskate.files.write, "skater_now.json", text)
    end
end)


-- A card on the game's Skaters page: whether the preset on screen is saved in the Fit Catalogue, with pictures or
-- not, its name and a few pieces. The app writes lineup_status.json with every slot's status and what its skater
-- wears; the preset on screen is the one whose skater matches what is worn now, so the card follows you as you
-- browse the presets, without waiting for the game to save which one is selected.
local hcache = {}
local function h32(s) -- the game's hash of a name (djb2 with xor), as the app computes it
    if s == nil or s == "" then return 0 end
    local c = hcache[s]
    if c then return c end
    local h = 5381
    for i = 1, #s do
        h = bit32.bxor((h * 33) % 4294967296, string.byte(s, i))
    end
    hcache[s] = h
    return h
end

local status, status_next, status_raw = nil, 0, ""
card = nil -- what the card shows (declared above): {slot, kind, name, parts, changed}
local card_next = 0
reskate.on("tick", function()
    local t = reskate.time.now()
    if t < card_next then return end
    card_next = t + 400
    local okp, onp = pcall(on_skaters_page)
    if not (okp and onp) then card = nil return end
    if t >= status_next then
        status_next = t + 1500
        local ok, text = pcall(reskate.files.read, "lineup_status.json")
        if ok and type(text) == "string" and text ~= "" and text ~= status_raw then
            local okd, st = pcall(reskate.json.decode, text)
            if okd and type(st) == "table" and type(st.slots) == "table" then status, status_raw = st, text end
        end
    end
    if not status then card = nil return end
    local worn, _, n = skater_slots()
    if n < 20 then return end -- keep what the card showed while the list is empty for a moment
    local now = {}
    for name, item in pairs(worn) do now[tostring(h32(name))] = h32(item) end
    local best, bestMiss = nil, 1e9
    for _, s in ipairs(status.slots) do
        if type(s.skater) == "table" then
            local miss, total = 0, 0
            for slot, item in pairs(s.skater) do
                local have = now[slot]
                if have ~= nil then
                    total = total + 1
                    if have ~= item then miss = miss + 1 end
                end
            end
            if total > 0 and (miss < bestMiss or (miss == bestMiss and s.slot == status.selected)) then
                best, bestMiss = s, miss
            end
        end
    end
    if not best or bestMiss > 20 then card = nil return end -- a skater that isn't one of the 10 presets
    local kind
    -- Saved means exactly the saved fit: any change makes it a new fit, not saved yet.
    if bestMiss > 0 or (type(best.changed) == "table" and #best.changed > 0) then kind = "unsaved"
    elseif best.saved and best.pics then kind = "saved"
    elseif best.saved then kind = "nopics"
    else kind = "unsaved" end
    card = {slot = best.slot, kind = kind, name = (if kind == "unsaved" and bestMiss > 0 then "New fit" else best.name) or "", parts = (type(best.parts) == "table" and best.parts) or {}}
end)

-- Where things sit on the Skaters page. The game lays its menu out on a 16:9 frame in the middle of the screen,
-- scaled to the screen's height; these are fractions of that frame (measured on a 2560x1440 screenshot).
local CARD = {x1 = 1815 / 2560, x2 = 2425 / 2560, y1 = 180 / 1440, y2 = 342 / 1440} -- above the Board panel
-- right of the skater; its right edge is as far from the Board panel (which starts at 1821) as the game's panels
-- are from each other (Board to Gestures: 36 px at 1440p)
local LIST = {x1 = 1515 / 2560, x2 = 1785 / 2560, y1 = 355 / 1440, y2 = 1225 / 1440}
local PANEL = "#1A1A1AFF" -- the colour of the game's own panels (Board, Gestures...)

-- s cut down (with "...") until it fits w pixels in that style.
local function fit(d, s, style, w)
    s = tostring(s or "")
    if d:measure(s, style).x <= w then return s end
    local n = #s
    while n > 1 do
        n = n - 1
        local t = string.sub(s, 1, n) .. "..."
        if d:measure(t, style).x <= w then return t end
    end
    return "..."
end

reskate.on("draw", function(d)
    if not card or job or cover then return end
    local okm, m = pcall(reskate.menu.game)
    if not okm or type(m) ~= "table" or not (m.open and m.skaters) then return end
    if type(reskate.menu.grid) == "function" then
        local okg, g = pcall(reskate.menu.grid)
        if okg and g ~= nil then return end -- picking items: out of the way
    end
    local size = d:size()
    if size.x <= 0 or size.y <= 0 then return end
    local k = size.y / 1080
    local fw = math.min(size.x, size.y * 16 / 9) -- the 16:9 frame the menu sits in
    local fx = (size.x - fw) / 2
    local function box(r) return fx + r.x1 * fw, r.y1 * size.y, fx + r.x2 * fw, r.y2 * size.y end
    local styles = {
        saved = {"#7BD88FFF", "Saved in your Fit Catalogue"},
        nopics = {"#F5B446FF", "Saved, no pictures: " .. snap_hint() .. " to take them"},
        unsaved = {"#9AA0AAFF", "Not saved: " .. snap_hint() .. " to add it"},
    }
    local st = styles[card.kind]

    -- the card: slot, status, name
    local l, t, r, b = box(CARD)
    d:rect(v(l, t), v(r, b), {colour = PANEL, fill = true}) -- square, like the game's panels
    d:rect(v(l, t), v(l + 4 * k, b), {colour = st[1], fill = true})
    local inner = r - l - 32 * k
    local head = {font = "bold", size = math.floor(13 * k + 0.5), colour = "#5ECDFAFF"}
    local line = {font = "bold", size = math.floor(17 * k + 0.5), colour = st[1]}
    local title = {font = "bold", size = math.floor(19 * k + 0.5), colour = "#F5F5F5FF"}
    local rows = {{"PRESET WARDROBE · SLOT " .. tostring(card.slot), head}, {st[2], line}, {card.name, title}}
    local gap = (b - t - 20 * k) / #rows
    local y = t + 10 * k
    for _, row in ipairs(rows) do
        d:text(v(l + 18 * k, y + (gap - row[2].size) / 2), fit(d, row[1], row[2], inner), row[2])
        y = y + gap
    end

    -- the list: every piece, its category small above its name
    if #card.parts == 0 then return end
    l, t, r, b = box(LIST)
    local cat = {size = math.floor(12 * k + 0.5), colour = "#9AA0AAFF"}
    local nm = {font = "bold", size = math.floor(15 * k + 0.5), colour = "#F5F5F5FF"}
    local per = cat.size + nm.size + 12 * k
    local shown = math.min(#card.parts, math.floor((b - t - 16 * k) / per))
    local h = shown * per + 16 * k
    d:rect(v(l, t), v(r, t + h), {colour = PANEL, fill = true, rounding = 8 * k})
    inner = r - l - 24 * k
    y = t + 10 * k
    for i = 1, shown do
        local p = card.parts[i]
        local label = tostring(p.c or "")
        if p.b and label ~= "" then label = label .. " · board" end
        if i == shown and shown < #card.parts then
            d:text(v(l + 12 * k, y), string.format("+ %d more", #card.parts - shown + 1), cat)
            break
        end
        d:text(v(l + 12 * k, y), fit(d, label, cat, inner), cat)
        d:text(v(l + 12 * k, y + cat.size + 2 * k), fit(d, p.t, nm, inner), nm)
        y = y + per
    end
end)

-- While Y is held on the Skaters page: a bar filling along the bottom of the card, so you know to keep holding.
-- With no card (the app isn't running), the card's spot shows the bar and what it's for.
reskate.on("draw", function(d)
    if not pad_hold.since or pad_hold.fired then return end
    local f = math.clamp((now() - pad_hold.since) / PAD_HOLD_MS, 0, 1)
    local size = d:size()
    if size.x <= 0 or size.y <= 0 then return end
    local k = size.y / 1080
    local fw = math.min(size.x, size.y * 16 / 9)
    local fx = (size.x - fw) / 2
    local l, t, r, b = fx + CARD.x1 * fw, CARD.y1 * size.y, fx + CARD.x2 * fw, CARD.y2 * size.y
    local shown = card and not job and not cover
    if not shown then
        d:rect(v(l, t), v(r, b), {colour = PANEL, fill = true})
        local label = if job then "KEEP HOLDING " .. PAD_NAME .. " TO CANCEL" else "KEEP HOLDING " .. PAD_NAME .. " TO SNAP"
        local style = {font = "bold", size = math.floor(17 * k + 0.5), colour = "#5ECDFAFF"}
        d:text(v(l + 18 * k, (t + b) / 2 - style.size / 2 - 4 * k), label, style)
    end
    d:rect(v(l, b - 6 * k), v(r, b), {colour = "#2A2D33FF", fill = true})
    d:rect(v(l, b - 6 * k), v(l + (r - l) * f, b), {colour = "#5ECDFAFF", fill = true})
end)
