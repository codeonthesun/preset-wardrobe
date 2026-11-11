-- Preset Trigger Lab: throwaway test. Press F8 in the world (or the menu button, then close the menu).
-- For each trigger: the companion app flips the helmet in the selected preset's saved data, the
-- plugin fires one in-game action, then watches outfit.slots() for the helmet to change.
local VERSION = "test 1"
local function now() return reskate.time.now() end
local run_n = (reskate.storage.get("run") or 0) + 1
reskate.storage.set("run", run_n)
local lines, dirty = {}, false
local function log(t) table.insert(lines, t) dirty = true end
local function flush()
    if not dirty then return end
    dirty = false
    pcall(reskate.files.write, "out/lab_" .. run_n .. ".txt", "Preset Trigger Lab " .. VERSION .. "\n" .. table.concat(lines, "\n") .. "\n")
end
local function say(t) pcall(reskate.ui.toast, string.sub(t, 1, 160), "info") end

local function hat()
    local ok, list = pcall(reskate.outfit.slots)
    if not ok or type(list) ~= "table" then return "?" end
    for _, e in list do
        if tostring(e.slot) == "cust_hats" then return tostring(e.item) end
    end
    return "(none)"
end
local function empty(h) return h == "(none)" or h == "" or h == "nil" end

local pos0 = nil
local function pos() local ok, p = pcall(reskate.skater.position) if ok and p then pos0 = p end return pos0 end
local function C(f, ...) local ok, a, b = pcall(f, ...) return string.format("%s/%s/%s", tostring(ok), tostring(a), tostring(b)) end

local triggers = {
    {"control (nothing)", function() return "-" end},
    {"teleport in place (on_board)", function() local p = pos() if not p then return "no pos" end return C(reskate.skater.teleport, p, nil, {on_board = true}) end},
    {"teleport in place (off board)", function() local p = pos() if not p then return "no pos" end return C(reskate.skater.teleport, p, nil, {on_board = false}) end},
    {"hide on", function() return C(reskate.skater.hide, true) end, function() return C(reskate.skater.hide, false) end},
    {"freeze on", function() return C(reskate.skater.freeze, true) end, function() return C(reskate.skater.freeze, false) end},
    {"noclip on", function() return C(reskate.skater.noclip, true) end, function() return C(reskate.skater.noclip, false) end},
    {"wipeout (bail)", function() return C(reskate.skater.wipeout, 0.5) end},
    {"camera free on", function() return C(reskate.camera.free, true) end, function() return C(reskate.camera.free, false) end},
    {"camera cycle ready", function() return C(reskate.camera.cycle, "ready") end, function() return C(reskate.camera.cycle, nil) end},
    {"camera cycle active", function() return C(reskate.camera.cycle, "active") end, function() return C(reskate.camera.cycle, nil) end},
    {"camera look at skater", function()
        local p = pos() if not p then return "no pos" end
        return C(reskate.camera.look, p + vector.create(0, 1.5, 4), p + vector.create(0, 1, 0), 0)
    end, function() reskate.camera.release() return "release" end},
    {"on_top empty then clear", function() return C(reskate.outfit.on_top, {}) end, function() return C(reskate.outfit.on_top, nil) end},
}

local job = nil
local function start()
    if job then return say("Already running.") end
    lines = {}
    job = {i = 1, phase = "ask", at = now(), want = nil, req = (reskate.storage.get("req") or 0)}
    log("start; hat now: " .. hat())
    say("Trigger lab running (~2 min). Stay in the world and don't touch anything.")
end

reskate.on("tick", function()
    if not job then return end
    local t = triggers[job.i]
    if not t then
        log("done")
        say("Trigger lab done.")
        flush() job = nil return
    end
    local el = now() - job.at
    if job.phase == "ask" then
        job.before = hat()
        job.want = empty(job.before) and "on" or "off"
        job.req += 1
        reskate.storage.set("req", job.req)
        pcall(reskate.files.write, "lab_req.json", string.format('{"id":%d,"want":"%s"}', job.req, job.want))
        job.phase, job.at = "wait_ack", now()
    elseif job.phase == "wait_ack" then
        local ok, s = pcall(reskate.files.read, "lab_ack.json")
        if ok and s and string.find(s, '"id":' .. job.req .. ',', 1, true) then
            job.ackok = string.find(s, '"ok":true', 1, true) ~= nil
            log(string.format("[%d] %s: db helmet -> %s (app %s)", job.i, t[1], job.want, job.ackok and "wrote" or s))
            job.phase, job.at = "fire", now()
        elseif el > 4000 then
            log(string.format("[%d] %s: no answer from the app, stopping", job.i, t[1]))
            flush() job = nil
        end
    elseif job.phase == "fire" then
        if el < 1000 then return end
        local okf, rf = pcall(t[2])
        log("   fired: " .. tostring(rf))
        job.phase, job.at, job.seen = "watch", now(), false
    elseif job.phase == "watch" then
        local h = hat()
        local changed = (job.want == "on") ~= empty(h)
        if changed and not job.seen then
            job.seen = true
            log(string.format("   *** GAME NOTICED after %d ms: hat = %s", el, h))
        end
        if el > 5000 then
            if not job.seen then log("   no change; hat = " .. h) end
            if t[3] then local _, ru = pcall(t[3]) log("   undo: " .. tostring(ru)) end
            flush()
            job.i += 1
            job.phase = "ask"
            job.at = now() + 1500
        end
    end
end)

pcall(reskate.input.bind, "triggerlab", "F8")
reskate.on("action", function(n) if n == "triggerlab" then start() end end)
local armed = nil
reskate.on("tick", function() if armed and now() >= armed then armed = nil start() end end)
reskate.menu.page("Preset Trigger Lab", function(ui)
    ui:note("Throwaway test. Press F8 in the world, or press the button, close the menu and wait 10 s. Takes about 2 minutes.")
    if job then
        ui:note("Running...")
    elseif ui:button("go", "Start in 10 s", true) then
        armed = now() + 10000
        say("Close the menu now. Starts in 10 s.")
    end
end)

reskate.on("unload", function()
    pcall(reskate.skater.hide, false)
    pcall(reskate.skater.freeze, false)
    pcall(reskate.skater.noclip, false)
    pcall(reskate.camera.free, false)
    pcall(reskate.camera.release)
end)
