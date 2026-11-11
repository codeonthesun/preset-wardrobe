local function advance(ms)
    local stepms = 16
    local t_end = T + ms
    while T < t_end do
        T += stepms
        plant(stepms)
        if cap.state == "recording" then
            cap.taken += 1
            if cap.taken > (cap.skip or 0) then cap.keptn += 1 end
        end
        if cap.state == "saving" and T >= cap.saving_until then
            cap.state = "saved"
            files[cap.path] = "<png>"
        end
        for _, tm in timers do
            if tm.at and T >= tm.at then
                tm.fn()
                tm.at = if tm.every then T + tm.every else nil
            end
        end
        handlers.tick()
        handlers.draw(fakeD)
    end
end


local failures = 0
local function check(ok, what)
    if ok then print("ok   " .. what) else print("FAIL " .. what); failures += 1 end
end
local function snaps()
    local n = 0
    for p in files do if p:match("^snaps/fit%-%d+%.png$") then n += 1 end end -- covers: one per snap
    return n
end

local function count(pattern)
    local n = 0
    for p in files do if p:match(pattern) then n += 1 end end
    return n
end

-- One fit with F8, spinning a full turn while it records. Track how far the preview turns once it
-- has first faced front.
local travelled = 0
local plant0 = plant
local faced_front = false
plant = function(ms)
    local before = preview.angle
    plant0(ms)
    if math.abs(preview.angle) < math.rad(3) then faced_front = true end
    if faced_front then
        local d = (preview.angle - before + math.pi) % (2 * math.pi) - math.pi
        travelled += math.abs(d)
    end
end
page_build(fakeUI)
handlers.action("presetwardrobe_snap")
advance(30000)
check(saved_frames["snaps/fit-00001-turn.png"] == 24, "F8 with the turn on saves 24 frames, together in one file")
check(files["snaps/fit-00001.png"] ~= nil, "and a cover picture")
local index = files["snaps/index.json"] or ""
check(index:find('"file":"fit%-00001%.png"') ~= nil and index:find('"turn":"fit%-00001%-turn%.png"') ~= nil, "the snap, its cover and its turn are listed in snaps/index.json")
local deg = function(r) return math.floor(math.deg(r) + 0.5) end
check(deg(travelled) >= 355 and deg(travelled) <= 380, "it spun all the way round once (" .. deg(travelled) .. " degrees)")
check(math.abs(deg(preview.angle)) <= 3, "the preview ends facing front (at " .. deg(preview.angle) .. " degrees)")
check(turns[#turns] == 0, "the stick is let go")

-- With the turn off: one picture, as before.
slider_override.spin_deg = 0
page_build(fakeUI)
slider_override.spin_deg = nil
handlers.action("presetwardrobe_snap")
advance(8000)
check(files["snaps/fit-00002.png"] ~= nil and count("^snaps/fit%-00002%-") == 0, "turn 0 saves one picture")
slider_override.spin_deg = 360
page_build(fakeUI)
slider_override.spin_deg = nil

-- Leaving the Skaters page pauses a snap, coming back finishes it.
handlers.action("presetwardrobe_snap")
skaters = false
advance(3000)
skaters = true
advance(30000)
check(snaps() == 3, "a paused snap finishes on return")

-- Messages from Preset Wardrobe are shown once each.
files["added.json"] = "ack"
reskate.json.decode = function(t)
    if t == "ack" then return {version = 2, messages = {{id = 1, message = "Added A"}, {id = 2, message = "Added B"}}} end
    return {}
end
advance(4000)
local shown = 0
for _, t in toasts do if t:find("Added [AB]") then shown += 1 end end
check(shown == 2, "both catalogue messages shown, once each")

-- No reading from the game: one picture, no turning.
local pose_fn = reskate.room.pose
reskate.room.pose = function() return nil end
local before = #turns
handlers.action("presetwardrobe_snap")
advance(9000)
check(files["snaps/fit-00004.png"] ~= nil and count("^snaps/fit%-00004%-") == 0, "without a reading it takes one picture")
local pushed = false
for i = before + 1, #turns do if turns[i] ~= 0 then pushed = true end end
check(not pushed, "without a reading it doesn't turn the preview")
reskate.room.pose = pose_fn

-- Many snaps while Preset Wardrobe isn't there to file them: only the newest 20 are kept.
slider_override.spin_deg = 90
page_build(fakeUI)
slider_override.spin_deg = nil
for i = 1, 24 do
    handlers.action("presetwardrobe_snap")
    advance(16000)
end
local covers = 0
for p in files do if p:match("^snaps/fit%-%d+%.png$") then covers += 1 end end
check(covers == 20, "only the newest 20 single snaps are kept (" .. covers .. ")")
check(files["snaps/fit-00001.png"] == nil and files["snaps/fit-00001-turn.png"] == nil, "the oldest snap's cover and turn are removed")
slider_override.spin_deg = 360
page_build(fakeUI)
slider_override.spin_deg = nil

-- All 10 slots in a row, each with its frames.
press.start_all = true
page_build(fakeUI)
advance(320000)
check(count("^snaps/%d%d%-turn%.png$") == 10 and saved_frames["snaps/10-turn.png"] == 24, "the walk-through saves a 24-frame turn for all 10 slots")
check(math.abs(math.deg(preview.angle)) <= 3, "after the walk-through the preview faces front, not turned 10 times over")
check((files["snaps/index.json"] or ""):find('"file":"10%.png"') ~= nil, "slot 10 is listed with its cover")

-- Board pictures: with the stage and animations, an F8 snap adds 4 board views after its turn.
local room_state, left, plays, board_ok = "idle", 0, {}, true
local function aa(ax, t) local h = math.sin(t / 2) return {ax[1] * h, ax[2] * h, ax[3] * h, math.cos(t / 2)} end
local function qm(a, b) return {a[4]*b[1]+a[1]*b[4]+a[2]*b[3]-a[3]*b[2], a[4]*b[2]-a[1]*b[3]+a[2]*b[4]+a[3]*b[1],
    a[4]*b[3]+a[1]*b[2]-a[2]*b[1]+a[3]*b[4], a[4]*b[4]-a[1]*b[1]-a[2]*b[2]-a[3]*b[3]} end
local function qr(q, v) local a, b, c, w = q[1], q[2], q[3], q[4]
    local tx, ty, tz = 2*(b*v[3]-c*v[2]), 2*(c*v[1]-a*v[3]), 2*(a*v[2]-b*v[1])
    return {v[1]+w*tx+(b*tz-c*ty), v[2]+w*ty+(c*tx-a*tz), v[3]+w*tz+(a*ty-b*tx)} end
-- the clip's board leans 2.5 degrees and is turned 15.5 off the stage, its long axis along z
local crooked = qm(aa({0, 1, 0}, math.rad(15.5)), qm(aa({1, 0, 0}, math.rad(2.5)), aa({1, 0, 0}, math.rad(-90))))
local view_axes = {}
local open_angle = nil
reskate.room.open = function() open_angle = preview.angle room_state = "opening" return true end
reskate.room.state = function() return room_state end
reskate.room.leave = function() left += 1 room_state = "idle" end
reskate.animation = {
    game_clips = function(w) return {"animation/dingo/c_proto_ofb_gesture_boardbalance_exit_lfoot_01", "other"} end,
    info = function() return {duration = 1.5, frames = 46} end,
    trim = function() return "clip:1" end,
    sample = function(c, j) if j == "board" then return {position = vector.create(0.41, 0.62, 0.11), rotation = crooked} end
        return {position = vector.create(0, 0, 0), rotation = {0, 0, 0, 1}} end,
    edit = function(c, tracks, o)
        for _, t in tracks do if t.joint == "board" then
            local n = qm(crooked, t.keys[1].rotation)
            view_axes[o.name] = {long = qr(n, {0, 0, 1}), face = qr(n, {0, 1, 0}),
                centre = {0.41 + t.keys[1].offset.x, 0.62 + t.keys[1].offset.y, 0.11 + t.keys[1].offset.z}}
        end end
        return "edit:" .. o.name end,
    play = function(c, o) table.insert(plays, c) return board_ok end,
    stop = function() end,
}
local opener = handlers.tick
handlers.tick = function() if room_state == "opening" then room_state = "open" end opener() end
handlers.action("presetwardrobe_snap")
advance(30000)
local newest = 0
for p in files do local n = p:match("^snaps/fit%-(%d+)%.png$") if n then newest = math.max(newest, tonumber(n)) end end
local base = string.format("snaps/fit-%05d", newest)
check(saved_frames[base .. "-board.png"] == 4, "F8 adds the 4 board views, together in one file")
check(saved_frames[base .. "-turn.png"] == 24, "and still the 24-frame turn")
check((files["snaps/index.json"] or ""):find('"board_turn":"') ~= nil, "the board views are listed in snaps/index.json")
check(left == 1, "it leaves the stage once done")
check(open_angle ~= nil and math.abs(math.deg(open_angle)) <= 1.2, "the preview faces front before the board pictures (" .. tostring(open_angle and math.floor(math.deg(open_angle) + 0.5)) .. " degrees)")
local square = true
for k = 0, 3 do
    local a = view_axes["presetwardrobe_board_" .. k]
    if not a or math.abs(a.long[2]) < 0.9999 or math.abs(a.centre[1] - 0.41) > 1e-4 or math.abs(a.centre[3] - 0.11) > 1e-4 then square = false end
    -- the deck faces the stage turned by the trim and 90 degrees per view
    if a then
        local want = math.rad(-10) + k * math.pi / 2
        local got = math.atan2(a.face[1], a.face[3])
        local diff = math.abs(((got - want) + math.pi / 4) % (math.pi / 2) - math.pi / 4)
        if diff > 1e-3 then square = false end
    end
end
check(square, "each view stands the board straight, turns it in place, -10 degrees off square and 90 apart")
-- When the game won't play the animation, the fit is still snapped, without board views.
board_ok = false
handlers.action("presetwardrobe_snap")
advance(30000)
check(snaps() >= 1 and count("%-board%.png$") == 1, "if the animation won't play, the fit is snapped without board views")

-- The board test: only the 4 board pictures, named test-b1 to -b4, not listed for the app.
board_ok = true
local covers_before = snaps()
press.board_test = true
page_build(fakeUI)
advance(15000)
check(saved_frames["snaps/test-board.png"] == 4, "Test board pictures saves test-board.png with the 4 views")
check(snaps() == covers_before, "and no fit snap")

-- The walk-through with board pictures: the turns, then a pass where each slot is selected and F8
-- takes its board. The controller isn't held while moving between presets.
board_ok = true
left = 0
run_turns_moving = 0
local banner = ""
local text0 = fakeD.text
fakeD.text = function(self, at, t, ...) if type(t) == "string" and t:find("MOVE RIGHT") then banner = "move" elseif type(t) == "string" and t:find("HOLD STILL") then banner = "hold" end return text0(self, at, t, ...) end
local turn0 = reskate.room.turn
reskate.room.turn = function(v, ...)
    if v ~= 0 and banner == "move" then run_turns_moving += 1 end
    return turn0(v, ...)
end
press.start_all = true
page_build(fakeUI)
advance(320000)
check(run_turns_moving == 0, "the preview isn't turned (holding the controller) while you move to the next preset")
for k = 1, 10 do
    handlers.action("presetwardrobe_snap")
    advance(9000)
end
local wb = 0
for k = 1, 10 do if saved_frames[string.format("snaps/%02d-board.png", k)] == 4 then wb += 1 end end
check(wb == 10, "the board pass saves all 10 boards (" .. wb .. ")")
check((files["snaps/index.json"] or ""):find('"board_turn":"10%-board%.png"') ~= nil, "slot 10's board is listed in snaps/index.json")
check(left == 1, "the stage is left once, at the end of the pass (" .. left .. ")")

-- The stick test: F9 holds the left stick right, then reports.
local sticks = {}
local turn1 = reskate.room.turn
reskate.room.turn = function(v, stick) table.insert(sticks, {v, stick}) return turn1(v, stick) end
handlers.action("presetwardrobe_stick_right")
advance(2600)
local pushed_left_x = false
for _, s in sticks do if s[2] == "left_x" and s[1] > 0 then pushed_left_x = true end end
check(pushed_left_x and sticks[#sticks][1] == 0, "F9 pushes the left stick right, then lets go")
check(toasts[#toasts]:find("Stick test") ~= nil, "and says what happened (" .. toasts[#toasts] .. ")")

-- A hands-off walk-through: the "app" answers keys.json, pressing C (right) and Z (left), and the
-- preset (what's worn) follows. Outfits 1..10, back to 1, then a board for each.
local slot = 1
local presses = {}
local worn0 = reskate.outfit.worn
reskate.outfit.worn = function() return {{item = "Slot" .. slot}} end
local last_id = 0
local app_on = true
timers[#timers + 1] = {at = T + 150, every = 150, fn = function()
    if not app_on then return end
    local text = files["snaps/keys.json"]
    if not text then return end
    local id, key = text:match('"id":(%d+)'), text:match('"key":"(%a)"')
    id = tonumber(id)
    if not id or id <= last_id then return end
    last_id = id
    table.insert(presses, key)
    if key == "c" then slot = math.min(10, slot + 1) elseif key == "z" then slot = math.max(1, slot - 1) end
    files["snaps/keys_done.json"] = string.format('{"id":%d,"pressed":true}', id)
end}
reskate.json.decode = function(t)
    if type(t) == "string" and t:find('"pressed"') then
        return {id = tonumber(t:match('"id":(%d+)')), pressed = t:find('"pressed":true') ~= nil}
    end
    return {"Own_Hoodie1", "Own_pants6"}
end
for k in saved_frames do saved_frames[k] = nil end
files["snaps/keys.json"] = nil -- the app never presses an old request (it checks the file's age)
left = 0
press.start_all = true
page_build(fakeUI)
advance(500000)
local cs, zs = 0, 0
for _, k in presses do if k == "c" then cs += 1 elseif k == "z" then zs += 1 end end
local outfits, boards = 0, 0
for k = 1, 10 do
    if saved_frames[string.format("snaps/%02d-turn.png", k)] == 24 then outfits += 1 end
    if saved_frames[string.format("snaps/%02d-board.png", k)] == 4 then boards += 1 end
end
check(outfits == 10 and boards == 10, string.format("hands off: 10 outfits and 10 boards (%d, %d)", outfits, boards))
check(cs == 18 and zs == 9, string.format("the app pressed C 9 times for the outfits, Z 9 to go back, C 9 for the boards (C %d, Z %d)", cs, zs))
check(left == 1, "the stage is left once at the end")
reskate.outfit.worn = worn0

-- Calibration: Show Griptape puts view 3 on the stage, and moving a slider shows it again.
plays = {}
left = 0
press.cal_3 = true
page_build(fakeUI)
advance(1000)
check(plays[#plays] == "edit:presetwardrobe_board_2", "Show Griptape plays the griptape view (" .. tostring(plays[#plays]) .. ")")
local n_before = #plays
slider_override.board_tilt_a2 = 1.5
page_build(fakeUI)
slider_override.board_tilt_a2 = nil
advance(600)
check(#plays > n_before, "moving Tilt A shows it again")
press.cal_done = true
page_build(fakeUI)
check(left == 1, "Done leaves the stage")

-- Messages: a picture request from the app shows as a tab with a count, Take it now snaps marked
-- with the request, and Dismiss sends it back.
reskate.outfit.worn = worn0
reskate.json.decode = function(t)
    if type(t) == "string" and t:find('"requests"') then
        local out = {}
        for id, name, slot in t:gmatch('{"id":(%d+),"name":"([^"]*)","slot":(%d+)}') do
            table.insert(out, {id = tonumber(id), name = name, slot = tonumber(slot)})
        end
        return {requests = out}
    end
    return {"Own_Hoodie1", "Own_pants6"}
end
local toasts_before = #toasts
files["requests.json"] = '{"requests":[{"id":7,"name":"Red hoodie","slot":3},{"id":8,"name":"Blue fit","slot":0}],"version":1}'
advance(1600)
page_build(fakeUI)
check(cards_seen["MESSAGES (2)"] == true, "the page shows MESSAGES (2) at the top")
local announced = 0
for i = toasts_before + 1, #toasts do if toasts[i]:find("needs a new picture") then announced += 1 end end
check(announced == 2, "each new request is announced once")
press.take_7 = true
page_build(fakeUI)
advance(40000)
check((files["snaps/index.json"] or ""):find('"request":7') ~= nil, "Take it now snaps marked with its request")
press.dismiss_8 = true
page_build(fakeUI)
check((files["requests_dismissed.json"] or ""):find("8") ~= nil, "Dismiss sends the request back to the app")

-- Unsaved changes: what's worn isn't any saved slot (the app's saved_outfits.json). The snap stops
-- first; F8 again snaps anyway, without the board pictures (whose trip to the stage loses changes).
local dec0 = reskate.json.decode
reskate.json.decode = function(t)
    if type(t) == "string" and t:find('"slots"') then
        local slots = {}
        for slot, items in t:gmatch('{"slot":(%d+),"items":%[([^%]]*)%]}') do
            local list = {}
            for k in items:gmatch('"([^"]+)"') do table.insert(list, k) end
            table.insert(slots, {slot = tonumber(slot), items = list})
        end
        return {slots = slots}
    end
    return dec0(t)
end
-- what's worn (hoodie and deck) is in no one slot: each slot has one of them, with something else
files["saved_outfits.json"] = '{"slots":[{"slot":1,"items":["own_hoodie1","own_otherdeck"]},{"slot":2,"items":["own_otherhoodie","own_deck"]}],"version":1}'

for k in saved_frames do saved_frames[k] = nil end
local opened = 0
local open0 = reskate.room.open
reskate.room.open = function() opened += 1 return open0() end
local covers0 = snaps()
handlers.action("presetwardrobe_snap")
advance(3000)
check(snaps() == covers0 and toasts[#toasts]:find("hasn't saved") ~= nil, "a changed, unsaved outfit isn't snapped, and says why")
handlers.action("presetwardrobe_snap")
advance(30000)
check(toasts[#toasts]:find("Fit snapped") ~= nil and opened == 0, "F8 again snaps it anyway, without going to the stage for the board (" .. toasts[#toasts] .. ", stage " .. opened .. ")")
files["saved_outfits.json"] = '{"slots":[{"slot":1,"items":["own_hoodie1","own_deck","own_custbody_mmesoavg"]}],"version":1}'
handlers.action("presetwardrobe_snap")
advance(40000)
check(opened == 1, "a saved outfit snaps as usual, board pictures and all")
-- the game's default parts aren't in any saved preset, and don't count
files["saved_outfits.json"] = '{"slots":[{"slot":1,"items":["own_deck"]}],"version":1}'
local worn1 = reskate.outfit.board
reskate.outfit.board = function() return {{item = "Own_Deck"}, {item = "Own_WheelColor_Gen_Classic_00009"}} end
local before_t = #toasts
handlers.action("presetwardrobe_snap")
advance(40000)
local blocked = false
for i = before_t + 1, #toasts do if toasts[i]:find("isn't any saved") then blocked = true end end
check(not blocked and opened == 2, "a default part no preset saves (the stock wheel colour) doesn't stop a snap")
reskate.outfit.board = worn1
reskate.room.open = open0
files["saved_outfits.json"] = nil

if failures > 0 then error(failures .. " plugin check(s) failed") end
print("all plugin checks passed")
