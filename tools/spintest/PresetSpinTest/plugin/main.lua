-- Preset Spin Test: a throwaway test for Preset Wardrobe.
--
-- Question: how far does dragging the mouse across the Skaters page preview turn the skater, compared
-- with the controller stick? Preset Wardrobe (the app) does the dragging (a plugin can't move the
-- mouse); this plugin asks for each drag, then reads the skater's heading from the game
-- (room.pose) and takes pictures, and compares them.
--
-- Each trial: (1) the stick turns the skater to face front, picture F; (2) the app drags the mouse
-- sideways by a set distance over a set time, the heading is watched for a while afterwards (does it
-- keep turning once the mouse lets go?), picture M; (3) the stick turns the skater to the same heading
-- the mouse reached, picture C. If the pictures M and C match (compare close to a still frame's 0.1),
-- the heading reading is true to what's on screen. The result file lists degrees per pixel for every
-- trial, so a mouse turn can be worked out.
--
-- NEEDS: a controller turned on (the stick is the reference), the Skaters page open on any preset, the
-- game the window in front, and Preset Wardrobe (the app) running with its game folder set.
-- Output: <game folder>\Mods\.reskate_plugin_data\...PresetSpinTest\out\results.txt and log.txt

local VERSION = "test 6"
local function now() return reskate.time.now() end
local function clock()
    local ok, s = pcall(os.date, "%H:%M:%S")
    if ok and s then return s end
    return tostring(os.time and os.time() or 0)
end

-- ---- the log ------------------------------------------------------------------------------------
local lines, dirty, last_write = {}, false, -100000
local function log(text)
    table.insert(lines, clock() .. "  " .. text)
    if #lines > 3000 then table.remove(lines, 1) end
    dirty = true
    pcall(reskate.log, "[PresetSpinTest] " .. text)
end
local function flush(force)
    if not dirty then return end
    if not force and now() - last_write < 1500 then return end
    last_write, dirty = now(), false
    pcall(reskate.files.write, "out/log.txt", "Preset Spin Test " .. VERSION .. "\n" .. table.concat(lines, "\n") .. "\n")
end
local function say(text, level) pcall(reskate.ui.toast, string.sub(text, 1, 160), level or "info") end

local TAU = 2 * math.pi
local function wrap(a) return (a + math.pi) % TAU - math.pi end

-- ---- the preview's heading (as Preset Wardrobe reads it) ---------------------------------------------
local function heading()
    if type(reskate.room) ~= "table" or type(reskate.room.pose) ~= "function" then return nil end
    local ok, pose = pcall(reskate.room.pose, "skater")
    if not ok or type(pose) ~= "table" or type(pose.rotation) ~= "table" or #pose.rotation ~= 4 then return nil end
    local q = pose.rotation
    local a, b, c, w = q[1], q[2], q[3], q[4]
    local tx, ty, tz = 2 * (b * 1 - c * 0), 2 * (c * 0 - a * 1), 2 * (a * 0 - b * 0)
    local x, z = 0 + w * tx + b * tz - c * ty, 1 + w * tz + a * ty - b * tx
    if x * x + z * z < 0.5 then return nil end
    return wrap(math.atan2(x, z) + math.pi / 2)
end

local function on_skaters_page()
    local m = reskate.menu.game()
    return m and m.open and m.skaters
end

-- ---- steering with the stick (the same as Preset Wardrobe) ---------------------------------------------
local turn_sign = reskate.storage.get("turn_sign") or 1
local steer = {target = nil, prev = nil, worse = 0, still_since = nil}
local function steer_to(target, tolerance)
    tolerance = tolerance or math.rad(2.5)
    local h = heading()
    if h == nil then return nil end
    local err = wrap(target - h)
    if steer.target ~= target then steer.target, steer.prev, steer.worse, steer.still_since = target, nil, 0, nil end
    if math.abs(err) <= tolerance then
        reskate.room.turn(0)
        steer.still_since = steer.still_since or now()
        steer.prev = err
        return now() - steer.still_since >= 250
    end
    steer.still_since = nil
    if steer.prev ~= nil then
        if math.abs(err) > math.abs(steer.prev) + 0.003 then steer.worse += 1
        elseif math.abs(err) < math.abs(steer.prev) - 0.003 then steer.worse = 0 end
        if steer.worse >= 5 then
            turn_sign = -turn_sign
            reskate.storage.set("turn_sign", turn_sign)
            steer.worse = 0
        end
    end
    steer.prev = err
    local speed = math.clamp(math.abs(err) / math.rad(35), 0.15, 0.7)
    reskate.room.turn((if err > 0 then speed else -speed) * turn_sign)
    return false
end

-- ---- where the preview is on screen ---------------------------------------------------------------------
local DEFAULT_AREA = {0.27, 0.14, 0.735, 0.985} -- the Skaters page's preview skater, as Preset Wardrobe frames it
local area = table.clone(DEFAULT_AREA)
local screen = {w = 0, h = 0}
reskate.on("draw", function(d)
    local size = d:size()
    if size.x > 0 and size.y > 0 then screen.w, screen.h = size.x, size.y end
end)
local function preview_centre()
    -- the same box Preset Wardrobe cuts its pictures from (the ultrawide menu is 21:9 at most)
    local w = math.min(screen.w, screen.h * 21 / 9)
    local left = (screen.w - w) / 2
    local x0, x1 = left + area[1] * w, left + area[3] * w
    local y0, y1 = area[2] * screen.h, area[4] * screen.h
    return ((x0 + x1) / 2) / screen.w, ((y0 + y1) / 2) / screen.h
end

-- ---- the trials -----------------------------------------------------------------------------------------
-- dx in pixels (of the game's own size), ms the drag takes, mode "abs" (the cursor is placed each step)
-- or "rel" (the cursor is moved by amounts, as a real mouse does)
local TRIALS = {
    {dx = 100, ms = 400, mode = "abs"}, {dx = 200, ms = 400, mode = "abs"}, {dx = 400, ms = 400, mode = "abs"},
    {dx = -200, ms = 400, mode = "abs"}, {dx = 200, ms = 400, mode = "abs"},
    {dx = 200, ms = 150, mode = "abs"}, {dx = 200, ms = 1000, mode = "abs"},
    {dx = 200, ms = 400, mode = "rel"}, {dx = 400, ms = 400, mode = "rel"}, {dx = -200, ms = 400, mode = "rel"},
}
local AFTER_MS = 1800   -- how long the heading is watched after the mouse lets go
local ANSWER_MS = 4000
local mouse_seq = reskate.storage.get("mouse_seq") or 0

local run = nil
local rows = {}

local function capture_options(skip)
    return {area = {area[1], area[2], area[3], area[4]}, frame = "menu", depth = true, background = true,
        height = 0, skip = skip, frames = 1200, seconds = 120}
end

local function finish(reason)
    reskate.room.turn(0)
    if run and run.capturing then pcall(reskate.capture.discard) end
    local out = {"Preset Spin Test " .. VERSION .. " (" .. clock() .. ")", reason, ""}
    table.insert(out, "trial  mode  drag px   ms   heading(deg)  deg/px   at release   keeps turning(deg)   cmp F-M   cmp C-M   cmp F-C")
    local sums = {}
    for i, r in rows do
        table.insert(out, string.format("%4d  %5s  %7d  %5d  %12.1f  %7.4f  %11.1f  %18.1f  %8.2f  %8.2f  %8.2f", i, r.mode, r.dx, r.ms, r.deg, r.per_px, r.at_release, r.after, r.d_fm, r.d_cm, r.d_fc))
        local key = r.mode
        sums[key] = sums[key] or {n = 0, total = 0}
        sums[key].n += 1
        sums[key].total += math.abs(r.per_px)
    end
    table.insert(out, "")
    for mode, s in sums do
        local mean = s.total / s.n
        table.insert(out, string.format("%s: mean %.4f deg per pixel over %d trials, so a full turn takes about %.0f px", mode, mean, s.n, 360 / math.max(mean, 1e-6)))
    end
    table.insert(out, "")
    table.insert(out, "heading at release = how far it had turned when the app let go of the mouse; 'keeps turning' = the turn that followed after that (momentum).")
    table.insert(out, "cmp = capture.compare of the pictures (0 = same; a still frame measured 0.11 before): F front, M after the mouse drag, C the stick turned to M's heading.")
    pcall(reskate.files.write, "out/results.txt", table.concat(out, "\n") .. "\n")
    log("finished: " .. reason)
    flush(true)
    say("Spin test done: " .. reason, "info")
    run = nil
end

local function mark()
    local kept = reskate.capture.kept()
    return kept[#kept]
end

local function compare(a, b)
    if a == nil or b == nil or type(reskate.capture.compare) ~= "function" then return -1 end
    local ok, d = pcall(reskate.capture.compare, a, b)
    if ok and type(d) == "number" then return d end
    return -1
end

local function next_trial()
    run.i += 1
    local t = TRIALS[run.i]
    if not t then return finish("all " .. #TRIALS .. " trials done") end
    run.t = {spec = t, phase = "home", since = now()}
    steer.target = nil
    log(string.format("trial %d: drag %d px over %d ms (%s)", run.i, t.dx, t.ms, t.mode))
end

local function step()
    if not run then return end
    local menu_up = on_skaters_page()
    if not menu_up then return finish("stopped: the Skaters page closed") end
    local elapsed = (now() - run.since) / 1000
    if run.phase == "warm" then
        local status = reskate.capture.state()
        if status.state == "failed" then return finish("capture failed: " .. tostring(status.message)) end
        if (status.taken or 0) >= 20 then
            local ok, why = reskate.capture.start(capture_options(2))
            if not ok then return finish("couldn't start the capture: " .. tostring(why)) end
            run.phase = "go"
            next_trial()
        elseif elapsed > 10 then
            return finish("no frames came in; keep the game window visible")
        end
        return
    end
    local status = reskate.capture.state()
    if status.state == "failed" then return finish("capture failed: " .. tostring(status.message)) end
    local t = run.t
    local spec = t.spec
    local age = (now() - t.since) / 1000
    if t.phase == "home" then
        local r = steer_to(0, math.rad(1))
        if r == nil then return finish("the game gives no heading (room.pose), so nothing can be measured") end
        if r == true then
            t.f_mark = mark()
            t.since, t.phase = now(), "drag"
            t.requested = false
        elseif age > 12 then
            return finish("the stick didn't turn the skater to the front (is a controller on?)")
        end
    elseif t.phase == "drag" then
        if not t.requested then
            local fx, fy = preview_centre()
            mouse_seq += 1
            reskate.storage.set("mouse_seq", mouse_seq)
            reskate.files.write("mouse.json", reskate.json.encode({id = mouse_seq, fx = fx, fy = fy, dx = spec.dx, dy = 0,
                ms = spec.ms, mode = spec.mode, w = screen.w, h = screen.h}))
            t.requested, t.id, t.asked = true, mouse_seq, now()
            reskate.room.turn(0)
            t.h0 = heading()
            t.travel, t.last_h, t.samples = 0, t.h0, {}
        end
        local h = heading()
        if h ~= nil and t.last_h ~= nil then
            local d = wrap(h - t.last_h)
            if math.abs(d) < math.pi / 2 then t.travel += d end
            t.last_h = h
        end
        table.insert(t.samples, {now() - t.asked, math.deg(t.travel)})
        local ok, text = pcall(reskate.files.read, "mouse_done.json")
        local ans = ok and type(text) == "string" and text ~= "" and select(2, pcall(reskate.json.decode, text)) or nil
        if type(ans) == "table" and ans.id == t.id then
            if not ans.done then return finish("the app couldn't drag the mouse: " .. tostring(ans.why)) end
            t.released_at, t.at_release = now(), math.deg(t.travel)
            t.phase = "after"
        elseif now() - t.asked > ANSWER_MS + spec.ms then
            return finish("no answer from Preset Wardrobe (is the app running, with its game folder set?)")
        end
    elseif t.phase == "after" then
        local h = heading()
        if h ~= nil and t.last_h ~= nil then
            local d = wrap(h - t.last_h)
            if math.abs(d) < math.pi / 2 then t.travel += d end
            t.last_h = h
        end
        table.insert(t.samples, {now() - t.asked, math.deg(t.travel)})
        if now() - t.released_at >= AFTER_MS then
            t.deg = math.deg(t.travel)
            t.m_mark = mark()
            t.target = wrap(t.last_h or 0)
            t.phase, t.since = "match", now()
            steer.target = nil
            log(string.format("   mouse turned it %.1f deg (%.4f deg/px); %.1f of that after release", t.deg, t.deg / spec.dx, t.deg - t.at_release))
            local peek = {}
            for k = 1, #t.samples, math.max(1, math.floor(#t.samples / 14)) do
                table.insert(peek, string.format("%d:%.0f", t.samples[k][1], t.samples[k][2]))
            end
            log("   heading over time (ms:deg): " .. table.concat(peek, " "))
        end
    elseif t.phase == "match" then
        local r = steer_to(t.target, math.rad(1.5))
        if r == true or age > 12 then
            t.c_mark = mark()
            table.insert(rows, {mode = spec.mode, dx = spec.dx, ms = spec.ms, deg = t.deg, per_px = t.deg / spec.dx,
                at_release = t.at_release, after = t.deg - t.at_release,
                d_fm = compare(t.f_mark, t.m_mark), d_cm = compare(t.c_mark, t.m_mark), d_fc = compare(t.f_mark, t.c_mark)})
            local row = rows[#rows]
            log(string.format("   pictures: F-M %.2f, C-M %.2f, F-C %.2f", row.d_fm, row.d_cm, row.d_fc))
            next_trial()
        end
    end
    flush(false)
end

local function start_run()
    if run then return say("Already running.", "warn") end
    if not on_skaters_page() then return say("Open the Skaters page first.", "warn") end
    if screen.w <= 0 then return say("Wait a moment, then try again.", "warn") end
    rows = {}
    lines = {}
    log("spin test started (screen " .. string.format("%dx%d", screen.w, screen.h) .. ")")
    local ok, why = reskate.capture.start(capture_options(0))
    if not ok then return say("Couldn't start the capture: " .. tostring(why), "warn") end
    run = {phase = "warm", since = now(), i = 0, capturing = true}
    say("Spin test running: keep the game in front and don't touch the mouse or stick.", "info")
end


-- ---- the new input API (ReSkate 2.1.4): can input.write turn the skater with NO controller? --------------
-- Each phase holds one kind of input for 2.5 s and records how far the heading moved and what the game said.
local ITEST = {
    {name = "room.turn 0.5 (what Preset Wardrobe uses now)", kind = "turn", v = 0.5},
    {name = "input.write left_x +0.7", kind = "write", pad = {left_x = 0.7}},
    {name = "input.write right_x +0.7", kind = "write", pad = {right_x = 0.7}},
    {name = "input.write left_x +0.7, right_x +0.7", kind = "write", pad = {left_x = 0.7, right_x = 0.7}},
    {name = "input.write right_x -0.7", kind = "write", pad = {right_x = -0.7}},
    {name = "input.write left_x -0.7", kind = "write", pad = {left_x = -0.7}},
}
local itest = nil
local ilines = {}

local function ilog(text)
    table.insert(ilines, text)
    log(text)
    pcall(reskate.files.write, "out/input_results.txt", "Preset Spin Test " .. VERSION .. " (input API)\n" .. table.concat(ilines, "\n") .. "\n")
end

local function ifinish(reason)
    pcall(reskate.room.turn, 0)
    pcall(reskate.input.write, nil, 0)
    ilog(reason)
    say("Input test done: " .. reason, "info")
    itest = nil
end

local function istep()
    if not itest then return end
    if not on_skaters_page() then return ifinish("stopped: the Skaters page closed") end
    local ph = ITEST[itest.i]
    local h = heading()
    if h == nil then return ifinish("no heading from room.pose") end
    if itest.phase == "begin" then
        itest.h0, itest.last, itest.travel, itest.t0, itest.oks, itest.whys = h, h, 0, now(), 0, {}
        itest.phase = "run"
        return
    end
    local d = wrap(h - itest.last)
    if math.abs(d) < math.pi / 2 then itest.travel += d end
    itest.last = h
    if itest.phase == "run" then
        if ph.kind == "turn" then
            reskate.room.turn(ph.v)
        else
            local ok, res, why = pcall(reskate.input.write, ph.pad, 0)
            if not ok then
                itest.whys["error: " .. tostring(res)] = true
            elseif res then
                itest.oks += 1
            else
                itest.whys[tostring(why)] = true
            end
        end
        if now() - itest.t0 >= 2500 then
            pcall(reskate.room.turn, 0)
            pcall(reskate.input.write, nil, 0)
            local whys = {}
            for w in itest.whys do table.insert(whys, w) end
            ilog(string.format("%-52s turned %6.1f deg   write ok %d times   game said: %s", ph.name, math.deg(itest.travel), itest.oks, #whys > 0 and table.concat(whys, " | ") or "-"))
            itest.phase, itest.t0 = "rest", now()
        end
    elseif itest.phase == "rest" then
        if now() - itest.t0 >= 1200 then
            itest.i += 1
            itest.phase = "begin"
            if itest.i > #ITEST then ifinish("all phases done") end
        end
    end
end

local function start_itest()
    if itest or run then return say("Something is already running.", "warn") end
    if not on_skaters_page() then return say("Open the Skaters page first.", "warn") end
    ilines = {}
    local pad = nil
    local okp, p = pcall(reskate.input.gamepad)
    if okp and p then pad = p end
    local f = nil
    pcall(function() f = reskate.input.focus() end)
    ilog("input test started. gamepad() sees a controller: " .. tostring(pad ~= nil) .. "; focus: " .. (f and string.format("focused=%s interface_open=%s gameplay=%s", tostring(f.focused), tostring(f.interface_open), tostring(f.gameplay)) or "unknown"))
    local okm, m = pcall(reskate.input.mouse)
    ilog("input.mouse() on this page: " .. tostring(okm and m ~= nil))
    itest = {i = 1, phase = "begin"}
    say("Input test running (about 25 s). Don't touch the mouse or sticks.", "info")
end

-- ---- no heading? Probe what the game does give, then measure mouse turns from the pictures alone ----------
local plines = {}
local function plog(text)
    table.insert(plines, text)
    log(text)
    pcall(reskate.files.write, "out/probe.txt", "Preset Spin Test " .. VERSION .. " (probe)\n" .. table.concat(plines, "\n") .. "\n")
end
local function describe(v, depth)
    depth = depth or 0
    if type(v) ~= "table" then return tostring(v) end
    if depth >= 2 then return "{...}" end
    local out = {}
    local n = 0
    for k, x in v do
        n += 1
        if n > 14 then table.insert(out, "...") break end
        table.insert(out, tostring(k) .. "=" .. describe(x, depth + 1))
    end
    return "{" .. table.concat(out, ", ") .. "}"
end
local function probe_all()
    local function try(name, f)
        local ok, a, b = pcall(f)
        plog(string.format("%-34s ok=%s  %s  %s", name, tostring(ok), describe(a), b ~= nil and describe(b) or ""))
    end
    try("room.pose('skater')", function() return reskate.room.pose("skater") end)
    try("room.pose()", function() return reskate.room.pose() end)
    try("room.state()", function() return reskate.room.state() end)
    try("menu.game()", function() return reskate.menu.game() end)
    try("skater.frame()", function() return reskate.skater.frame() end)
    try("skater.animation_values()", function() return reskate.skater.animation_values() end)
    try("skater.movement()", function() return reskate.skater.movement() end)
    try("input.gamepad()", function() return reskate.input.gamepad() end)
    try("input.focus()", function() return reskate.input.focus() end)
end

local prun = nil
local STEPS, STEP_PX = 14, 100
local function pfinish(reason)
    if prun and prun.capturing then pcall(reskate.capture.discard) end
    local out = {"Preset Spin Test " .. VERSION .. " (pictures only) - " .. reason, ""}
    table.insert(out, "step  total drag px   cmp vs front   cmp vs last step   picture")
    if prun then
        for i, r in prun.rows do
            table.insert(out, string.format("%4d  %13d  %12.2f  %16.2f   %s", i - 1, r.px, r.vf, r.vl, r.file or "-"))
        end
    end
    pcall(reskate.files.write, "out/pictures.txt", table.concat(out, "\n") .. "\n")
    log("pictures run finished: " .. reason)
    say("Picture test done: " .. reason, "info")
    prun = nil
end

local function pstep()
    if not prun then return end
    if not on_skaters_page() then return pfinish("stopped: the Skaters page closed") end
    local status = reskate.capture.state()
    if status.state == "failed" and prun.phase ~= "save" then return pfinish("capture failed: " .. tostring(status.message)) end
    local el = now() - prun.since
    if prun.phase == "warm" then
        if (status.taken or 0) >= 20 then
            local ok, why = reskate.capture.start(capture_options(0))
            if not ok then return pfinish("couldn't start the capture: " .. tostring(why)) end
            prun.phase, prun.since = "settle", now()
        elseif el > 10000 then return pfinish("no frames came in") end
    elseif prun.phase == "settle" then
        if el >= 1500 then
            local m = mark()
            if prun.k == 0 then prun.front = m end
            local vf = prun.front and m and compare(prun.front, m) or 0
            local vl = prun.last and m and compare(prun.last, m) or 0
            table.insert(prun.rows, {px = prun.k * STEP_PX, vf = vf, vl = vl, mark = m})
            prun.last = m
            plog(string.format("step %d (%d px, next drags: %s): cmp vs front %.2f, vs last %.2f", prun.k, prun.k * STEP_PX, (if prun.k < 7 then "rel" else "abs"), vf, vl))
            if prun.k >= STEPS then
                prun.phase, prun.si, prun.since = "save", 0, now()
                return
            end
            prun.k += 1
            mouse_seq += 1
            reskate.storage.set("mouse_seq", mouse_seq)
            local fx, fy = preview_centre()
            reskate.files.write("mouse.json", reskate.json.encode({id = mouse_seq, fx = fx, fy = fy, dx = STEP_PX, dy = 0, ms = 400, mode = (if prun.k <= 7 then "rel" else "abs"), w = screen.w, h = screen.h}))
            prun.id, prun.phase, prun.since = mouse_seq, "drag", now()
        end
    elseif prun.phase == "drag" then
        local ok, text = pcall(reskate.files.read, "mouse_done.json")
        local ans = ok and type(text) == "string" and text ~= "" and select(2, pcall(reskate.json.decode, text)) or nil
        if type(ans) == "table" and ans.id == prun.id then
            if not ans.done then return pfinish("the app couldn't drag: " .. tostring(ans.why)) end
            prun.phase, prun.since = "settle", now()
        elseif el > 6000 then return pfinish("no answer from the app (is Preset Wardrobe running?)") end
    elseif prun.phase == "save" then
        if prun.saving then
            if status.state == "saved" then
                prun.rows[prun.si].file = prun.saving
                prun.saving = nil
            elseif status.state == "failed" then return pfinish("a picture couldn't be saved: " .. tostring(status.message))
            elseif el > 20000 then return pfinish("saving timed out") end
            return
        end
        prun.si += 1
        local r = prun.rows[prun.si]
        if not r then return pfinish("done") end
        prun.saving = "pictest_" .. string.format("%02d", prun.si - 1)
        local ok, why = reskate.capture.save("pictest/" .. prun.saving, {frames = {r.mark}, format = "png", size = 512,
            background = "transparent", cutout = "auto", solid = false})
        if not ok then return pfinish("couldn't save: " .. tostring(why)) end
        prun.since = now()
    end
end

local function start_pics()
    if itest or run or prun then return say("Something is already running.", "warn") end
    if not on_skaters_page() then return say("Open the Skaters page first.", "warn") end
    if screen.w <= 0 then return say("Wait a moment, then try again.", "warn") end
    plines = {}
    probe_all()
    local ok, why = reskate.capture.start(capture_options(0))
    if not ok then return say("Couldn't start the capture: " .. tostring(why), "warn") end
    prun = {phase = "warm", since = now(), k = 0, rows = {}, capturing = true}
    say("Picture test running (about 1 minute): the app drags the mouse. Hands off.", "info")
end

-- Starting from a menu button would drag the mouse across that menu, so a start is armed: a countdown
-- while you get to the Skaters page (or press F7 there to start the picture test at once).
local armed = nil
local function arm(fn, label)
    armed = {fn = fn, at = now() + 15000, told = 15, label = label}
    say(label .. " starts in 15 s. Close this menu and open the Skaters page now; then hands off.", "info")
end
local function arm_tick()
    if not armed then return end
    local left = math.ceil((armed.at - now()) / 1000)
    if left <= 5 and left < armed.told and left > 0 then
        armed.told = left
        say(armed.label .. " in " .. left .. "...", "info")
    end
    if now() >= armed.at then
        local f = armed.fn
        armed = nil
        f()
    end
end
pcall(reskate.input.bind, "pictest", "F7")
reskate.on("action", function(name)
    if name == "pictest" then
        armed = nil
        start_pics()
    end
end)

reskate.on("tick", function()
    arm_tick()
    local pok, perr = pcall(pstep)
    if not pok then log("picture test error: " .. tostring(perr)); pfinish("error: " .. tostring(perr)) end
    local iok, ierr = pcall(istep)
    if not iok then log("input test error: " .. tostring(ierr)); itest = nil end
    local ok, err = pcall(step)
    if not ok then
        log("error: " .. tostring(err))
        finish("stopped by an error: " .. tostring(err))
    end
end)

reskate.menu.page("Preset Spin Test", function(ui)
    ui:note("Press a button, then close the menu and open the Skaters page within 15 s (or press F7 on the Skaters page for the picture test). Throwaway test. Turn a controller on, open the Skaters page, then run it. About 2 minutes: the app drags the mouse over the preview, so keep your hands off.")
    if prun then
        ui:note("Picture test running: step " .. tostring(prun.k) .. " of " .. STEPS)
    elseif ui:button("pics", "Picture test: drag the mouse, no controller needed") then
        arm(start_pics, "Picture test")
    end
    if itest then
        ui:note("Input test running...")
    elseif ui:button("itest", "Test the new input API (no controller needed)") then
        arm(start_itest, "Input test")
    end
    if run then
        ui:note(string.format("Running: trial %d of %d.", math.max(run.i, 1), #TRIALS))
        if ui:button("cancel", "Cancel") then finish("cancelled") end
    elseif ui:button("run", "Run the spin test", true) then
        arm(start_run, "Spin test")
    end
end)

reskate.on("unload", function()
    pcall(reskate.room.turn, 0)
    flush(true)
end)
