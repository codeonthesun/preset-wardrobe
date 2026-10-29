-- Preset Wardrobe plugin: pictures of your skater presets for the Preset Wardrobe app.
--
-- F8 on the game's Skaters page snaps the selected preset as one fit for your Fit Catalogue.
-- The plugin's page can also walk through all 10 slots in a row.
--
-- Pictures go to <game folder>\Mods\.reskate_plugin_data\PresetWardrobe\snaps\, where
-- Preset Wardrobe picks them up by itself.

local VERSION = "0.2.0"
local DEFAULT_AREA = {0.27, 0.14, 0.735, 0.985} -- the Skaters page's preview skater (as Fit Check frames it)

-- ---- settings ---------------------------------------------------------------------------------
local defaults = {hold = 3, move = 2, count = 10, area = table.clone(DEFAULT_AREA), framing_preview = false}
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

local function file_for(job)
    if job.mode == "test" then return "snaps/test.png" end
    if job.mode == "fit" then return string.format("snaps/fit-%05d.png", job.seq) end
    return string.format("snaps/%02d.png", job.n)
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

local function worn_keys()
    local keys = {}
    for _, it in reskate.outfit.worn() do table.insert(keys, it.item) end
    for _, it in reskate.outfit.board() do table.insert(keys, it.item) end
    return keys
end

local function enter(phase)
    job.phase = phase
    job.since = now()
end

local function stop_job(message, level)
    if job and (job.phase == "warm" or job.phase == "record") then reskate.capture.discard() end
    job = nil
    if message then say(message, level) end
end

-- mode: "fit" (the selected preset, for the Fit Catalogue), "all" (slots 1 to 10 in a row) or "test".
local function start_job(mode)
    if job then return stop_job("Snapping cancelled.") end
    job = {n = 1, mode = mode, test = mode == "test", run = wall_clock(), phase = "wait_menu", since = now()}
    if mode == "all" then run_shots = {} end
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
        height = 640, skip = skip, frames = 2400, seconds = 60}
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

    -- Left the Skaters page: drop any capture and wait to come back to the same preset.
    if not menu_up and job.phase ~= "saving" then
        if job.phase == "warm" or job.phase == "record" then reskate.capture.discard() end
        enter("wait_menu")
        say(string.format("Paused. Open the Skaters page on preset %d to carry on.", job.n), "warn")
        return
    end

    if job.phase == "move" then
        if elapsed >= settings.move then enter("hold") end

    elseif job.phase == "hold" then
        if elapsed >= settings.hold then
            -- Warm-up capture first: the game's depth is only copied once frames are being taken
            -- (what Fit Check found), so the real capture starts straight after it.
            local ok, why = reskate.capture.start(capture_options(0))
            if not ok then return stop_job("Could not start the picture: " .. tostring(why), "warn") end
            job.worn = worn_keys()
            if job.mode == "fit" then
                fit_seq += 1
                job.seq = fit_seq
                reskate.storage.set("fit_seq", fit_seq)
            end
            enter("warm")
        end

    elseif job.phase == "warm" then
        local status = reskate.capture.state()
        if status.state == "failed" then return stop_job("Capture failed: " .. tostring(status.message), "warn") end
        if (status.taken or 0) >= 20 then
            local ok, why = reskate.capture.start(capture_options(2))
            if not ok then return stop_job("Could not start the picture: " .. tostring(why), "warn") end
            enter("record")
        elseif elapsed > 10 then
            return stop_job("No frames came in for the picture. Keep the game window visible and try again.", "warn")
        end

    elseif job.phase == "record" then
        local status = reskate.capture.state()
        if status.state == "failed" then return stop_job("Capture failed: " .. tostring(status.message), "warn") end
        local kept = reskate.capture.kept()
        if #kept >= 3 then
            local ok, why = reskate.capture.save(file_for(job), {frames = {kept[#kept]}, format = "png",
                size = 512, background = "transparent", cutout = "auto", solid = false})
            if not ok then return stop_job("Could not save the picture: " .. tostring(why), "warn") end
            enter("saving")
        elseif elapsed > 10 then
            return stop_job("The picture took too long. Try again.", "warn")
        end

    elseif job.phase == "saving" then
        local status = reskate.capture.state()
        if status.state == "saved" then
            reskate.capture.discard()
            if job.test then
                return stop_job("Test picture saved as snaps/test.png. OPEN SNAPS FOLDER to check the framing.")
            end
            if job.mode == "fit" then
                table.insert(singles, {seq = job.seq, file = string.format("fit-%05d.png", job.seq), worn = job.worn,
                    at = wall_clock(), depth = status.depth == true})
                while #singles > 30 do table.remove(singles, 1) end
                reskate.storage.set("singles", singles)
                write_index()
                return stop_job("Fit snapped! Preset Wardrobe adds it to your Fit Catalogue.")
            end
            run_shots[job.n] = {n = job.n, file = string.format("%02d.png", job.n), worn = job.worn, at = wall_clock(),
                depth = status.depth == true}
            write_index()
            if job.n >= settings.count then
                enter("done")
                write_index()
                return stop_job(string.format("All %d slots snapped! Preset Wardrobe adds them to your Fit Catalogue.", settings.count))
            end
            job.n += 1
            enter("move")
        elseif status.state == "failed" then
            reskate.capture.discard()
            return stop_job("The picture could not be saved: " .. tostring(status.message), "warn")
        elseif elapsed > 30 then
            return stop_job("Saving the picture timed out.", "warn")
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
reskate.time.after(2000, function() pcall(answer_titles) end)

reskate.on("tick", step)

-- ---- F8 -----------------------------------------------------------------------------------------
reskate.input.bind("presetwardrobe_snap", "F8")
reskate.on("action", function(name)
    if name == "presetwardrobe_snap" then start_job("fit") end
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
        detail = "Select the preset to snap. F8 cancels."
    elseif single and job.phase == "hold" then
        local left = math.max(0, settings.hold - (now() - job.since) / 1000)
        title = string.format("%s  -  HOLD STILL  %d", if job.test then "TEST PICTURE" else "SNAPPING THIS FIT", math.ceil(left))
        detail = "Keep this preset selected. F8 cancels."
    elseif single then
        title = if job.test then "TAKING THE TEST PICTURE..." else "SNAPPING FIT..."
        detail = "Hands off for a moment."
    elseif job.phase == "wait_menu" then
        title = if job.n == 1 then "OPEN THE SKATERS PAGE" else "PAUSED"
        detail = if job.n == 1 then "Highlight your first (leftmost) preset. F8 cancels."
            else string.format("Go back to the Skaters page on preset %d. F8 cancels.", job.n)
    elseif job.phase == "hold" then
        local left = math.max(0, settings.hold - (now() - job.since) / 1000)
        title = string.format("PRESET %d OF %d  -  HOLD STILL  %d", job.n, count, math.ceil(left))
        detail = if job.test then "Test picture of the highlighted preset." else "Keep this preset highlighted. F8 cancels."
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
reskate.menu.page("Preset Wardrobe", function(ui)
    ui:card("SNAP A FIT", "Adds a preset to your Fit Catalogue in Preset Wardrobe")
    ui:label("1. Close this menu and open the game's Skaters page.")
    ui:label("2. Select the preset you want to keep and press F8.")
    ui:label("3. Hold still while the banner counts down. That's it.")
    if job then
        ui:info("Now", if job.mode == "all" then string.format("slot %d of %d (%s)", job.n, settings.count, job.phase)
            else job.phase)
        if ui:button("cancel", "Cancel snapping", true) then stop_job("Snapping cancelled.") end
    else
        if ui:button("start", "Snap the selected preset", true) then start_job("fit") end
    end
    ui:info("Last", last_message)

    ui:card("ALL 10 IN A ROW", "Snap every slot in one go")
    ui:note("Highlight your first (leftmost) slot, then follow the banner: hold still, move one to the right, repeat.")
    if not job and ui:button("start_all", "Walk through all slots") then start_job("all") end
    local done = 0
    for i = 1, settings.count do if run_shots[i] then done += 1 end end
    ui:bar(string.format("%d / %d snapped this session", done, settings.count), done, 0, settings.count)

    ui:card("TIMING", "How long the banner gives you")
    local hold = math.floor(ui:slider("hold", "Hold still for", settings.hold, 1, 8, "%d s") + 0.5)
    local move = math.floor(ui:slider("move", "Time to move to the next", settings.move, 1, 6, "%d s") + 0.5)
    local count = math.floor(ui:slider("count", "Slots in a walk-through", settings.count, 1, 10, "%d") + 0.5)
    if hold ~= settings.hold or move ~= settings.move or count ~= settings.count then
        settings.hold, settings.move, settings.count = hold, move, count
        save_settings()
    end

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
end)

reskate.on("unload", function()
    if job then reskate.capture.discard() end
end)
