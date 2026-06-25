// Hidden oracle for bug4 (fix e21d79ab): From impls for common library errors.
use cf_modkit_errors::CanonicalError;

#[test]
fn from_io_error_produces_internal() {
    let io_err = std::io::Error::new(std::io::ErrorKind::NotFound, "file missing");
    let err = CanonicalError::from(io_err);
    assert_eq!(err.status_code(), 500);
    assert_eq!(err.title(), "Internal");
}
#[test]
fn from_serde_json_error_produces_invalid_argument() {
    let json_err = serde_json::from_str::<serde_json::Value>("not json").unwrap_err();
    let msg = json_err.to_string();
    let err = CanonicalError::from(json_err);
    assert_eq!(err.status_code(), 400);
    assert_eq!(err.title(), "Invalid Argument");
    assert_eq!(err.detail(), msg);
}
#[test]
fn question_mark_propagation_serde_json() {
    fn inner() -> Result<serde_json::Value, CanonicalError> {
        Ok(serde_json::from_str("{invalid")?)
    }
    assert_eq!(inner().unwrap_err().status_code(), 400);
}
