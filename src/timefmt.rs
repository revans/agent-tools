//! Slack timestamps: `ts` <-> epoch, ISO-8601 input, and output in the reader's own zone.
//!
//! A Slack `ts` is epoch seconds with microseconds ("1718031234.567890"). Reading a channel
//! means showing those as a local wall clock, so the system zoneinfo file is parsed here
//! rather than taking on a date library for two conversions.

use anyhow::{Result, bail};
use std::path::PathBuf;
use std::sync::OnceLock;

/// Whole seconds of a Slack `ts`; `None` when it is not a timestamp at all.
pub fn ts_seconds(ts: &str) -> Option<i64> {
    let head = ts.split('.').next()?;
    if head.is_empty() || !head.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    head.parse().ok()
}

/// `--since` / `--until`: a raw Slack `ts`, bare epoch seconds, or an ISO-8601 date/datetime.
pub fn parse_when(input: &str) -> Result<String> {
    let s = input.trim();
    if s.is_empty() {
        bail!("empty timestamp");
    }
    // A ts or bare epoch goes straight through; Slack compares these as decimal strings.
    if s.bytes().all(|b| b.is_ascii_digit() || b == b'.') && s.matches('.').count() <= 1 {
        let secs = ts_seconds(s).ok_or_else(|| anyhow::anyhow!("not a timestamp: \"{input}\""))?;
        return Ok(match s.split_once('.') {
            Some((_, frac)) if !frac.is_empty() => s.to_string(),
            _ => format!("{secs}.000000"),
        });
    }
    let secs = parse_iso8601(s)?;
    Ok(format!("{secs}.000000"))
}

/// Accepts `YYYY-MM-DD`, `YYYY-MM-DDTHH:MM[:SS[.fff]]`, a space instead of `T`, and a
/// trailing `Z` or `±HH[:MM]`. Without a zone the value is read as local time.
pub fn parse_iso8601(s: &str) -> Result<i64> {
    let bad = || {
        anyhow::anyhow!(
            "could not read \"{s}\" as a time; use a Slack ts (1718031234.567890), \
             a date (2024-06-10) or an ISO-8601 datetime (2024-06-10T14:30:00Z)"
        )
    };
    let bytes = s.as_bytes();
    if bytes.len() < 10 || bytes[4] != b'-' || bytes[7] != b'-' {
        return Err(bad());
    }
    let year: i64 = s[0..4].parse().map_err(|_| bad())?;
    let month: i64 = s[5..7].parse().map_err(|_| bad())?;
    let day: i64 = s[8..10].parse().map_err(|_| bad())?;
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return Err(bad());
    }

    let mut rest = &s[10..];
    let (mut hour, mut min, mut sec) = (0i64, 0i64, 0i64);
    let mut offset: Option<i64> = None;

    if !rest.is_empty() {
        let sep = rest.as_bytes()[0];
        if !matches!(sep, b'T' | b't' | b' ') {
            return Err(bad());
        }
        rest = &rest[1..];
        // Split the zone suffix off before reading the clock.
        let zone_at = rest
            .char_indices()
            .find(|(i, c)| matches!(*c, 'Z' | 'z' | '+') || (*c == '-' && *i > 0))
            .map(|(i, _)| i);
        let (clock, zone) = match zone_at {
            Some(i) => (&rest[..i], &rest[i..]),
            None => (rest, ""),
        };
        let mut parts = clock.split(':');
        hour = parts
            .next()
            .filter(|p| !p.is_empty())
            .ok_or_else(bad)?
            .parse()
            .map_err(|_| bad())?;
        if let Some(p) = parts.next() {
            min = p.parse().map_err(|_| bad())?;
        }
        if let Some(p) = parts.next() {
            // Fractional seconds are accepted and dropped; Slack's resolution stops here.
            let whole = p.split('.').next().unwrap_or("0");
            sec = whole.parse().map_err(|_| bad())?;
        }
        if parts.next().is_some() {
            return Err(bad());
        }
        offset = match zone.chars().next() {
            None => None,
            Some('Z' | 'z') if zone.len() == 1 => Some(0),
            Some(sign @ ('+' | '-')) => {
                let body = zone[1..].replace(':', "");
                if body.len() != 2 && body.len() != 4 {
                    return Err(bad());
                }
                let h: i64 = body[0..2].parse().map_err(|_| bad())?;
                let m: i64 = if body.len() == 4 {
                    body[2..4].parse().map_err(|_| bad())?
                } else {
                    0
                };
                let mag = h * 3600 + m * 60;
                Some(if sign == '-' { -mag } else { mag })
            }
            _ => return Err(bad()),
        };
    }
    if hour > 23 || min > 59 || sec > 60 {
        return Err(bad());
    }

    let naive = days_from_civil(year, month, day) * 86_400 + hour * 3600 + min * 60 + sec;
    Ok(match offset {
        Some(off) => naive - off,
        None => epoch_from_local(naive),
    })
}

/// RFC 3339 in the reader's zone, e.g. `2024-06-10T14:33:54-06:00`.
pub fn iso_local(secs: i64) -> String {
    let (off, _) = offset_at(secs);
    let (y, mo, d, h, mi, s) = civil(secs + off as i64);
    let (sign, off_abs) = if off < 0 { ('-', -off) } else { ('+', off) };
    format!(
        "{y:04}-{mo:02}-{d:02}T{h:02}:{mi:02}:{s:02}{sign}{:02}:{:02}",
        off_abs / 3600,
        (off_abs % 3600) / 60
    )
}

/// What a person wants to see next to a message: `2024-06-10 14:33 MDT`.
pub fn human_local(secs: i64) -> String {
    let (off, abbrev) = offset_at(secs);
    let (y, mo, d, h, mi, _) = civil(secs + off as i64);
    format!("{y:04}-{mo:02}-{d:02} {h:02}:{mi:02} {abbrev}")
}

fn civil(secs: i64) -> (i64, u32, u32, u32, u32, u32) {
    let days = secs.div_euclid(86_400);
    let rem = secs.rem_euclid(86_400);
    let (y, mo, d) = civil_from_days(days);
    (
        y,
        mo,
        d,
        (rem / 3600) as u32,
        ((rem % 3600) / 60) as u32,
        (rem % 60) as u32,
    )
}

/// Days since 1970-01-01 for a proleptic Gregorian date (Howard Hinnant's algorithm).
fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    (if m <= 2 { y + 1 } else { y }, m as u32, d as u32)
}

/// A local wall clock read as if it were UTC, turned into a real instant. Two passes,
/// because the offset itself depends on the instant we are solving for.
fn epoch_from_local(naive: i64) -> i64 {
    let first = naive - offset_at(naive).0 as i64;
    naive - offset_at(first).0 as i64
}

// ------------------------------------------------------------------ zoneinfo

/// Seconds east of UTC and the zone abbreviation in force at `secs`.
pub fn offset_at(secs: i64) -> (i32, String) {
    match local_tz() {
        Some(tz) => tz.lookup(secs),
        None => (0, "UTC".to_string()),
    }
}

struct Tz {
    /// Transition instants, ascending, parallel to `idx`.
    transitions: Vec<i64>,
    idx: Vec<u8>,
    /// (seconds east of UTC, abbreviation)
    types: Vec<(i32, String)>,
}

impl Tz {
    fn lookup(&self, secs: i64) -> (i32, String) {
        let fallback = || {
            // Before the first transition, the first non-DST type is the convention.
            self.types
                .iter()
                .find(|t| !t.1.is_empty())
                .cloned()
                .unwrap_or((0, "UTC".to_string()))
        };
        if self.transitions.is_empty() {
            return fallback();
        }
        let pos = match self.transitions.binary_search(&secs) {
            Ok(i) => i,
            Err(0) => return fallback(),
            Err(i) => i - 1,
        };
        match self.idx.get(pos).and_then(|i| self.types.get(*i as usize)) {
            Some(t) => t.clone(),
            None => fallback(),
        }
    }
}

fn local_tz() -> Option<&'static Tz> {
    static TZ: OnceLock<Option<Tz>> = OnceLock::new();
    TZ.get_or_init(|| {
        let path = tz_path()?;
        let bytes = std::fs::read(path).ok()?;
        parse_tzif(&bytes)
    })
    .as_ref()
}

/// `$TZ` as a zoneinfo name (the POSIX rule form is not supported), else `/etc/localtime`.
fn tz_path() -> Option<PathBuf> {
    if let Ok(tz) = std::env::var("TZ") {
        let tz = tz.trim_start_matches(':');
        if !tz.is_empty()
            && !tz.starts_with('/')
            && tz
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'/' | b'_' | b'-' | b'+'))
            && !tz.contains("..")
        {
            let p = PathBuf::from("/usr/share/zoneinfo").join(tz);
            if p.is_file() {
                return Some(p);
            }
        }
        if tz.starts_with('/') && PathBuf::from(tz).is_file() {
            return Some(PathBuf::from(tz));
        }
        return None;
    }
    let p = PathBuf::from("/etc/localtime");
    p.exists().then_some(p)
}

/// Enough of RFC 8536 to answer "what was the offset at this instant".
fn parse_tzif(b: &[u8]) -> Option<Tz> {
    if b.len() < 44 || &b[0..4] != b"TZif" {
        return None;
    }
    let version = b[4];
    let counts = |at: usize| -> Option<[usize; 6]> {
        let mut c = [0usize; 6];
        for (i, slot) in c.iter_mut().enumerate() {
            let o = at + 20 + i * 4;
            *slot = u32::from_be_bytes(b.get(o..o + 4)?.try_into().ok()?) as usize;
        }
        Some(c)
    };

    let [isutc, isstd, leap, timecnt, typecnt, charcnt] = counts(0)?;
    let (at, time_size) = if version >= b'2' {
        // Version 1 data is a 32-bit prefix; the 64-bit block after it is the usable one.
        let v1 = timecnt * 5 + typecnt * 6 + charcnt + leap * 8 + isstd + isutc;
        let at = 44 + v1;
        if b.get(at..at + 4)? != b"TZif" {
            return None;
        }
        (at, 8usize)
    } else {
        (0usize, 4usize)
    };
    let [isutc, isstd, leap, timecnt, typecnt, charcnt] = counts(at)?;
    let _ = (isutc, isstd, leap);

    let mut p = at + 44;
    let mut transitions = Vec::with_capacity(timecnt);
    for _ in 0..timecnt {
        let raw = b.get(p..p + time_size)?;
        transitions.push(match time_size {
            8 => i64::from_be_bytes(raw.try_into().ok()?),
            _ => i32::from_be_bytes(raw.try_into().ok()?) as i64,
        });
        p += time_size;
    }
    let idx = b.get(p..p + timecnt)?.to_vec();
    p += timecnt;

    let mut raw_types = Vec::with_capacity(typecnt);
    for _ in 0..typecnt {
        let off = i32::from_be_bytes(b.get(p..p + 4)?.try_into().ok()?);
        let abbr_idx = *b.get(p + 5)? as usize;
        raw_types.push((off, abbr_idx));
        p += 6;
    }
    let chars = b.get(p..p + charcnt)?;
    let types: Vec<(i32, String)> = raw_types
        .into_iter()
        .map(|(off, i)| {
            let name = chars
                .get(i..)
                .and_then(|s| s.split(|c| *c == 0).next())
                .map(|s| String::from_utf8_lossy(s).into_owned())
                .unwrap_or_default();
            (off, name)
        })
        .collect();
    if types.is_empty() {
        return None;
    }
    Some(Tz {
        transitions,
        idx,
        types,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn civil_round_trips() {
        for secs in [0i64, 1_718_031_234, 951_782_400, -86_400, 2_000_000_000] {
            let (y, mo, d, h, mi, s) = civil(secs);
            let back = days_from_civil(y, mo as i64, d as i64) * 86_400
                + h as i64 * 3600
                + mi as i64 * 60
                + s as i64;
            assert_eq!(back, secs, "{secs}");
        }
        assert_eq!(civil(0), (1970, 1, 1, 0, 0, 0));
        assert_eq!(civil(1_718_031_234), (2024, 6, 10, 14, 53, 54));
    }

    #[test]
    fn parses_zoned_iso() {
        assert_eq!(
            parse_iso8601("2024-06-10T14:53:54Z").unwrap(),
            1_718_031_234
        );
        assert_eq!(
            parse_iso8601("2024-06-10T16:53:54+02:00").unwrap(),
            1_718_031_234
        );
        assert_eq!(
            parse_iso8601("2024-06-10T08:53:54-0600").unwrap(),
            1_718_031_234
        );
        assert_eq!(parse_iso8601("2024-06-10 14:53Z").unwrap(), 1_718_031_180);
        assert_eq!(
            parse_iso8601("2024-06-10T14:53:54.123Z").unwrap(),
            1_718_031_234
        );
    }

    #[test]
    fn rejects_nonsense() {
        for bad in [
            "yesterday",
            "2024-13-01",
            "2024-06-10T25:00:00Z",
            "06/10/2024",
            "2024-06",
        ] {
            assert!(parse_iso8601(bad).is_err(), "{bad} should not parse");
        }
    }

    #[test]
    fn when_accepts_ts_and_dates() {
        assert_eq!(
            parse_when("1718031234.567890").unwrap(),
            "1718031234.567890"
        );
        assert_eq!(parse_when("1718031234").unwrap(), "1718031234.000000");
        assert_eq!(
            parse_when("2024-06-10T14:53:54Z").unwrap(),
            "1718031234.000000"
        );
        // A local date resolves to that day's midnight, whatever the host zone is.
        let midnight = parse_when("2024-06-10").unwrap();
        let secs = ts_seconds(&midnight).unwrap();
        let (off, _) = offset_at(secs);
        assert_eq!((secs + off as i64).rem_euclid(86_400), 0);
        assert!(parse_when("not-a-time").is_err());
    }

    #[test]
    fn ts_seconds_is_tolerant() {
        assert_eq!(ts_seconds("1718031234.567890"), Some(1_718_031_234));
        assert_eq!(ts_seconds("1718031234"), Some(1_718_031_234));
        assert_eq!(ts_seconds(""), None);
        assert_eq!(ts_seconds("p1718031234"), None);
    }

    #[test]
    fn local_formatting_is_self_consistent() {
        let iso = iso_local(1_718_031_234);
        assert_eq!(iso.len(), 25, "{iso}");
        assert_eq!(&iso[4..5], "-");
        assert!(human_local(1_718_031_234).starts_with("202"));
    }
}
