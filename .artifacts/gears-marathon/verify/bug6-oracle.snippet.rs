
#[test]
fn oracle_bug6_single_char_identifier_parses() {
    // Hidden oracle (fix ba166a57): a one-char identifier like `x` must parse.
    let result = parse_str("x eq 1");
    assert!(result.is_ok(), "single-char identifier should parse, got {result:?}");
}
