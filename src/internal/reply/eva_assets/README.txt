EVAChat Flow data pack  (data/eva_chat/)
=========================================

input/<category>.txt   = what the USER writes (1 trigger per line, '#' = comment)
output/<category>.txt  = what the BOT replies  (1 reply per line, '|' also splits)
Same file name = same category. Add a new input+output pair and it works at once.
countries.txt          = keyword=country (user country detection, {country} value)

Flow:  NEW -> WAIT_AGE_GENDER -> WAIT_COUNTRY -> MIDDLE -> END
  user first sms       -> greeting.txt   ("m21" first -> age_gender.txt)
  m21 / f 19           -> age_gender.txt (once)      no age yet -> age_ask.txt
  "where u from?"      -> country.txt    (once)      user country -> flirty_questions.txt
  MIDDLE: horny / flirty_reply / how_are_you / your_age / english_only / busy_later /
          warm_reply / greeting ... otherwise flirty_questions.txt
  horny question ("u horny?") -> horny_answer.txt + horny.txt (2 lines)
  after horny / flirty_reply  -> next bot line is ALWAYS ask_snap.txt
  yes after the ask           -> share_snap.txt (%username%) -> END
  user asks "ur sc?" anytime  -> share immediately -> END
  "no" after the ask          -> retry_nudge.txt once -> again "no" -> closer.txt
  8 bot replies               -> closer.txt -> END
  age under 18                -> never shares, closer.txt

Rules: every output file is used ONCE per user (except flirty_questions, horny,
middle_chat/flirty_reply, ask_snap, horny_answer, closer which repeat with a
different line). Never the same line twice in a row.

Placeholders: %username% or {snap} = a username from the dashboard Usernames list
              {country} = the user's detected country (default usa)

Restart the bot after editing. Delete this folder to re-seed the starter pack.
